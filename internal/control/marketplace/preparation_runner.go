package marketplacecontrol

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/theronburger/switchyard/internal/control/environment"
)

const (
	PreparationStdoutLog       = "stdout.log"
	PreparationStderrLog       = "stderr.log"
	defaultPreparationLogBytes = 4 * 1024 * 1024
	defaultPreparationGrace    = 2 * time.Second
	defaultPreparationKillWait = 2 * time.Second
	maximumDiagnosticTailBytes = 64 * 1024
	maximumDiagnosticBytes     = 768
)

var (
	ErrPreparationInvalid = errors.New("Marketplace preparation is invalid")
	ErrPreparationFailed  = errors.New("Marketplace preparation failed")
	typeScriptDiagnostic  = regexp.MustCompile(`([A-Za-z0-9_./-]+\.(?:ts|tsx))\((\d+),(\d+)\): error (TS\d+): ([^\r\n]+)`)
	ansiEscape            = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
)

type PreparationFailure struct {
	PreparationID string
	ServiceID     string
	Diagnostic    string
	LogReference  string
	ExitCode      *int
}

func (failure *PreparationFailure) Error() string {
	return ErrPreparationFailed.Error()
}

func (failure *PreparationFailure) Unwrap() error {
	return ErrPreparationFailed
}

func (failure *PreparationFailure) OperationFailure() environment.OperationFailure {
	message := "Service preparation failed."
	if failure.ServiceID != "" {
		message = failure.ServiceID + " preparation failed."
	}
	return environment.OperationFailure{
		Code: "SERVICE_PREPARATION_FAILED", Message: message, Retryable: false,
		ResourceKind: "service", ResourceID: failure.ServiceID,
		Step: failure.PreparationID, Diagnostic: failure.Diagnostic,
		LogReference: failure.LogReference, NextAction: "fix_service_build",
		ExitCode: failure.ExitCode,
	}
}

type OSPreparationRunner struct {
	MaximumLogBytes int64
	GracePeriod     time.Duration
	KillWait        time.Duration
}

func (runner OSPreparationRunner) Run(ctx context.Context, preparation environment.PreparationSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validOSPreparation(preparation) {
		return ErrPreparationInvalid
	}
	maximumLogBytes, gracePeriod, killWait, valid := runner.limits()
	if !valid {
		return ErrPreparationInvalid
	}
	if err := preparePreparationDirectory(preparation.RunDirectory); err != nil {
		return ErrPreparationInvalid
	}
	stdoutPath := filepath.Join(preparation.RunDirectory, PreparationStdoutLog)
	stdout, err := openPreparationLog(
		stdoutPath,
		maximumLogBytes,
	)
	if err != nil {
		return ErrPreparationInvalid
	}
	defer func() { _ = stdout.Close() }()
	stderrPath := filepath.Join(preparation.RunDirectory, PreparationStderrLog)
	stderr, err := openPreparationLog(
		stderrPath,
		maximumLogBytes,
	)
	if err != nil {
		return ErrPreparationInvalid
	}
	defer func() { _ = stderr.Close() }()

	preparationContext, cancel := context.WithTimeout(ctx, preparation.Timeout)
	defer cancel()
	command := exec.Command(preparation.Executable, preparation.Arguments...)
	command.Dir = preparation.Directory
	command.Env = append([]string(nil), preparation.Environment...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return ErrPreparationFailed
	}
	waited := make(chan error, 1)
	go func() {
		waited <- command.Wait()
	}()
	select {
	case err := <-waited:
		return preparationResult(preparationContext, err, preparation, stdoutPath, stderrPath)
	case <-preparationContext.Done():
		select {
		case err := <-waited:
			return preparationResult(preparationContext, err, preparation, stdoutPath, stderrPath)
		default:
		}
		terminationError := terminatePreparationGroup(command.Process.Pid, waited, gracePeriod, killWait)
		return errors.Join(preparationContext.Err(), terminationError)
	}
}

func (runner OSPreparationRunner) limits() (int64, time.Duration, time.Duration, bool) {
	maximumLogBytes := runner.MaximumLogBytes
	if maximumLogBytes == 0 {
		maximumLogBytes = defaultPreparationLogBytes
	}
	gracePeriod := runner.GracePeriod
	if gracePeriod == 0 {
		gracePeriod = defaultPreparationGrace
	}
	killWait := runner.KillWait
	if killWait == 0 {
		killWait = defaultPreparationKillWait
	}
	valid := maximumLogBytes > 0 && maximumLogBytes <= 64*1024*1024 &&
		gracePeriod > 0 && gracePeriod <= 30*time.Second &&
		killWait > 0 && killWait <= 30*time.Second
	return maximumLogBytes, gracePeriod, killWait, valid
}

func validOSPreparation(preparation environment.PreparationSpec) bool {
	if preparation.ID == "" || !cleanAbsolutePath(preparation.Executable) ||
		!cleanAbsolutePath(preparation.Directory) || !cleanAbsolutePath(preparation.RunDirectory) ||
		preparation.Timeout <= 0 || preparation.Timeout > 30*time.Minute {
		return false
	}
	executable, err := os.Lstat(preparation.Executable)
	if err != nil || !executable.Mode().IsRegular() || executable.Mode()&os.ModeSymlink != 0 ||
		executable.Mode().Perm()&0o111 == 0 {
		return false
	}
	directory, err := os.Lstat(preparation.Directory)
	if err != nil || !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 {
		return false
	}
	for _, argument := range preparation.Arguments {
		if argument == "" || len(argument) > 1024*1024 || strings.ContainsRune(argument, 0) {
			return false
		}
	}
	requiredEnvironment := map[string]bool{"HOME": false, "PATH": false, "TMPDIR": false}
	seenEnvironment := make(map[string]struct{}, len(preparation.Environment))
	for _, variable := range preparation.Environment {
		name, value, found := strings.Cut(variable, "=")
		if !found || name == "" || strings.ContainsRune(variable, 0) {
			return false
		}
		if _, duplicate := seenEnvironment[name]; duplicate {
			return false
		}
		seenEnvironment[name] = struct{}{}
		if _, required := requiredEnvironment[name]; required {
			if name == "PATH" && !validExecutablePath(value) ||
				name != "PATH" && !cleanAbsolutePath(value) {
				return false
			}
			requiredEnvironment[name] = true
		}
	}
	return requiredEnvironment["HOME"] && requiredEnvironment["PATH"] && requiredEnvironment["TMPDIR"]
}

func preparePreparationDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrPreparationInvalid
	}
	return os.Chmod(path, 0o700)
}

type preparationLog struct {
	file      *os.File
	remaining int64
}

func openPreparationLog(path string, maximumBytes int64) (*preparationLog, error) {
	file, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	stat, statOK := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !statOK || stat.Nlink != 1 {
		_ = file.Close()
		return nil, ErrPreparationInvalid
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &preparationLog{file: file, remaining: maximumBytes}, nil
}

func (log *preparationLog) Write(contents []byte) (int, error) {
	originalLength := len(contents)
	if log.remaining <= 0 {
		return originalLength, nil
	}
	if int64(len(contents)) > log.remaining {
		contents = contents[:log.remaining]
	}
	written, err := log.file.Write(contents)
	log.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	return originalLength, nil
}

func (log *preparationLog) Close() error {
	if log == nil || log.file == nil {
		return nil
	}
	if err := log.file.Sync(); err != nil {
		_ = log.file.Close()
		return err
	}
	return log.file.Close()
}

func preparationResult(
	ctx context.Context,
	err error,
	preparation environment.PreparationSpec,
	stdoutPath string,
	stderrPath string,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		var exitCode *int
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			code := exitError.ExitCode()
			exitCode = &code
		}
		return &PreparationFailure{
			PreparationID: preparation.ID,
			ServiceID:     preparation.ServiceID,
			Diagnostic:    safePreparationDiagnostic(preparation.Directory, stdoutPath, stderrPath),
			LogReference:  preparation.LogReference,
			ExitCode:      exitCode,
		}
	}
	return nil
}

func safePreparationDiagnostic(directory string, paths ...string) string {
	for pathIndex := len(paths) - 1; pathIndex >= 0; pathIndex-- {
		contents := readDiagnosticTail(paths[pathIndex])
		lines := strings.Split(string(contents), "\n")
		for lineIndex := len(lines) - 1; lineIndex >= 0; lineIndex-- {
			line := ansiEscape.ReplaceAllString(lines[lineIndex], "")
			match := typeScriptDiagnostic.FindStringSubmatch(line)
			if len(match) != 6 || unsafeDiagnostic(match[5]) {
				continue
			}
			path := match[1]
			if filepath.IsAbs(path) {
				relative, err := filepath.Rel(directory, path)
				if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					continue
				}
				path = filepath.ToSlash(relative)
			}
			diagnostic := path + ":" + match[2] + ":" + match[3] + ": " + match[4] + ": " + strings.TrimSpace(match[5])
			if len(diagnostic) > maximumDiagnosticBytes {
				diagnostic = diagnostic[:maximumDiagnosticBytes]
			}
			return diagnostic
		}
	}
	return ""
}

func readDiagnosticTail(path string) []byte {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	size := info.Size()
	start := size - maximumDiagnosticTailBytes
	if start < 0 {
		start = 0
	}
	contents := make([]byte, size-start)
	read, err := file.ReadAt(contents, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil
	}
	return contents[:read]
}

func unsafeDiagnostic(message string) bool {
	upper := strings.ToUpper(message)
	for _, forbidden := range []string{
		"SECRET", "TOKEN", "PASSWORD", "AUTHORIZATION", "COOKIE", "PRIVATE KEY", "AWS_",
	} {
		if strings.Contains(upper, forbidden) {
			return true
		}
	}
	for _, character := range message {
		if character < 0x20 && character != '\t' {
			return true
		}
	}
	_, err := strconv.Unquote(`"` + strings.ReplaceAll(message, `"`, `\"`) + `"`)
	return err != nil
}

func terminatePreparationGroup(
	processGroupID int,
	waited <-chan error,
	gracePeriod time.Duration,
	killWait time.Duration,
) error {
	_ = syscall.Kill(-processGroupID, syscall.SIGTERM)
	grace := time.NewTimer(gracePeriod)
	defer grace.Stop()
	select {
	case <-waited:
		return nil
	case <-grace.C:
	}
	_ = syscall.Kill(-processGroupID, syscall.SIGKILL)
	kill := time.NewTimer(killWait)
	defer kill.Stop()
	select {
	case <-waited:
		return nil
	case <-kill.C:
		return ErrPreparationFailed
	}
}

var _ io.Writer = (*preparationLog)(nil)
