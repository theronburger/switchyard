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

	"github.com/theronburger/switchyard/internal/runtime/childgroup"
)

const terminationGrace = 2 * time.Second

// ExactRunner executes a compiled command in a positively owned process group
// with bounded owner-only stdout/stderr files. It never consults a shell and
// never inherits the daemon environment. RuntimeRoot must be an existing,
// owned, non-symlinked directory; every run directory must sit beneath it and
// every path component between the two must be proven before it is touched.
type ExactRunner struct {
	RuntimeRoot string
	Now         func() time.Time
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
	if err := createOwnedRunDirectory(runner.RuntimeRoot, command.RunDirectory); err != nil {
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
	supervised := childgroup.Supervise(runContext, process, terminationGrace)
	waitErr := supervised.WaitErr
	if supervised.Interrupted {
		outcome.TimedOut = errors.Is(runContext.Err(), context.DeadlineExceeded)
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

// createOwnedRunDirectory proves that root is an owned private directory and
// that every existing component between root and destination is a real owned
// private directory before creating the missing remainder. Nothing under a
// symlinked or foreign component is ever created or modified.
func createOwnedRunDirectory(root, destination string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsRune(root, 0) {
		return ErrInvalidCommand
	}
	relative, err := filepath.Rel(root, destination)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrInvalidCommand
	}
	if !ownedPrivateDirectory(root) {
		return ErrInvalidCommand
	}
	components := strings.Split(relative, string(filepath.Separator))
	current := root
	create := len(components)
	for index, component := range components {
		candidate := filepath.Join(current, component)
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			create = index
			break
		}
		if err != nil || !ownedPrivateDirectory(candidate) {
			return ErrInvalidCommand
		}
		current = candidate
	}
	for _, component := range components[create:] {
		current = filepath.Join(current, component)
		if err := os.Mkdir(current, 0o700); err != nil {
			return ErrInvalidCommand
		}
		if !ownedPrivateDirectory(current) {
			return ErrInvalidCommand
		}
	}
	return nil
}

func ownedPrivateDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0o077 == 0
}

type boundedLog struct {
	file      *os.File
	remaining int64
	truncated bool
}

// openBoundedLog creates a fresh owner-only log or, when one already exists,
// opens it without following symlinks and proves it is a singly linked owned
// regular file before truncating it. A foreign or multiply linked file is
// never opened for writing with truncation.
func openBoundedLog(path string) (*boundedLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, os.ErrExist) {
		file, err = os.OpenFile(path, os.O_WRONLY|syscall.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, ErrInvalidCommand
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, ErrInvalidCommand
	}
	if err := file.Truncate(0); err != nil {
		_ = file.Close()
		return nil, err
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
