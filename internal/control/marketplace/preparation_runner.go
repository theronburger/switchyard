package marketplacecontrol

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
)

var (
	ErrPreparationInvalid = errors.New("Marketplace preparation is invalid")
	ErrPreparationFailed  = errors.New("Marketplace preparation failed")
)

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
	stdout, err := openPreparationLog(
		filepath.Join(preparation.RunDirectory, PreparationStdoutLog),
		maximumLogBytes,
	)
	if err != nil {
		return ErrPreparationInvalid
	}
	defer stdout.Close()
	stderr, err := openPreparationLog(
		filepath.Join(preparation.RunDirectory, PreparationStderrLog),
		maximumLogBytes,
	)
	if err != nil {
		return ErrPreparationInvalid
	}
	defer stderr.Close()

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
		return preparationResult(preparationContext, err)
	case <-preparationContext.Done():
		select {
		case err := <-waited:
			return preparationResult(preparationContext, err)
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

func preparationResult(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return ErrPreparationFailed
	}
	return nil
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
