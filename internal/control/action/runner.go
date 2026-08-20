package action

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const terminationGrace = 2 * time.Second

// ExactRunner executes a compiled command in a positively owned process group
// with bounded owner-only stdout/stderr files. It never consults a shell and
// never inherits the daemon environment.
type ExactRunner struct {
	Now func() time.Time
}

// Run executes the command and reports its bounded outcome. A non-zero exit
// status is reported through Outcome, not through the error; errors are
// reserved for invalid commands, start failures, and runner faults.
func (runner ExactRunner) Run(ctx context.Context, command ExactCommand) (Outcome, error) {
	now := runner.Now
	if now == nil {
		now = time.Now
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := validateCommand(command); err != nil {
		return Outcome{}, err
	}
	if err := os.MkdirAll(command.RunDirectory, 0o700); err != nil {
		return Outcome{}, err
	}
	stdout, err := openBoundedLog(filepath.Join(command.RunDirectory, "stdout.log"))
	if err != nil {
		return Outcome{}, err
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := openBoundedLog(filepath.Join(command.RunDirectory, "stderr.log"))
	if err != nil {
		return Outcome{}, err
	}
	defer func() { _ = stderr.Close() }()

	runContext, cancel := context.WithTimeout(ctx, command.Timeout)
	defer cancel()
	process := exec.Command(command.Executable, command.Arguments...)
	process.Dir = command.Directory
	process.Env = append([]string(nil), command.Environment...)
	process.Stdin = nil
	process.Stdout = stdout
	process.Stderr = stderr
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	outcome := Outcome{StartedAt: now().UTC()}
	if err := process.Start(); err != nil {
		return Outcome{}, ErrCommandStart
	}
	waited := make(chan error, 1)
	go func() { waited <- process.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waited:
	case <-runContext.Done():
		outcome.TimedOut = errors.Is(runContext.Err(), context.DeadlineExceeded)
		_ = syscall.Kill(-process.Process.Pid, syscall.SIGTERM)
		select {
		case waitErr = <-waited:
		case <-time.After(terminationGrace):
			_ = syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
			select {
			case waitErr = <-waited:
			case <-time.After(terminationGrace):
				waitErr = errors.New("profile action command did not exit")
			}
		}
		if !outcome.TimedOut {
			return Outcome{}, runContext.Err()
		}
	}
	outcome.FinishedAt = now().UTC()
	outcome.StdoutTruncated = stdout.truncated
	outcome.StderrTruncated = stderr.truncated
	outcome.ExitCode = exitCode(process, waitErr)
	return outcome, nil
}

func exitCode(process *exec.Cmd, waitErr error) int {
	if process.ProcessState != nil {
		if code := process.ProcessState.ExitCode(); code >= 0 {
			return code
		}
		if status, ok := process.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
	}
	if waitErr == nil {
		return 0
	}
	return -1
}

func validateCommand(command ExactCommand) error {
	for _, path := range []string{command.Executable, command.Directory, command.RunDirectory} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
			return ErrInvalidCommand
		}
	}
	info, err := os.Lstat(command.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return ErrInvalidCommand
	}
	directory, err := os.Lstat(command.Directory)
	if err != nil || !directory.IsDir() {
		return ErrInvalidCommand
	}
	if command.Timeout <= 0 || command.Timeout > MaximumTimeout {
		return ErrInvalidCommand
	}
	if len(command.Arguments) > 1024 {
		return ErrInvalidCommand
	}
	for _, argument := range command.Arguments {
		if strings.ContainsRune(argument, 0) {
			return ErrInvalidCommand
		}
	}
	required := map[string]bool{"HOME": false, "PATH": false, "TMPDIR": false}
	seen := make(map[string]struct{}, len(command.Environment))
	for _, entry := range command.Environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" || strings.ContainsRune(entry, 0) {
			return ErrInvalidCommand
		}
		if _, duplicate := seen[name]; duplicate {
			return ErrInvalidCommand
		}
		seen[name] = struct{}{}
		if _, tracked := required[name]; tracked {
			required[name] = true
		}
	}
	if !required["HOME"] || !required["PATH"] || !required["TMPDIR"] {
		return ErrInvalidCommand
	}
	return nil
}

type boundedLog struct {
	file      *os.File
	remaining int64
	truncated bool
}

func openBoundedLog(path string) (*boundedLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
		_ = file.Close()
		return nil, ErrInvalidCommand
	}
	return &boundedLog{file: file, remaining: MaximumOutputBytes}, nil
}

func (log *boundedLog) Write(contents []byte) (int, error) {
	original := len(contents)
	if log.remaining <= 0 {
		if original > 0 {
			log.truncated = true
		}
		return original, nil
	}
	if int64(len(contents)) > log.remaining {
		contents = contents[:log.remaining]
		log.truncated = true
	}
	written, err := log.file.Write(contents)
	log.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	return original, nil
}

func (log *boundedLog) Close() error {
	if log == nil || log.file == nil {
		return nil
	}
	return errors.Join(log.file.Sync(), log.file.Close())
}
