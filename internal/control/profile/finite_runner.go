package profile

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

	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"

	"github.com/theronburger/switchyard/internal/runtime/childgroup"
)

const maximumFiniteLogBytes = 4 * 1024 * 1024

type FiniteRunner struct{}

func (FiniteRunner) Run(ctx context.Context, step environmentcontrol.PreparationSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validFiniteStep(step) {
		return ErrProfileInvalid
	}
	if err := os.MkdirAll(step.RunDirectory, 0o700); err != nil {
		return err
	}
	stdout, err := newBoundedLog(filepath.Join(step.RunDirectory, "stdout.log"))
	if err != nil {
		return err
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := newBoundedLog(filepath.Join(step.RunDirectory, "stderr.log"))
	if err != nil {
		return err
	}
	defer func() { _ = stderr.Close() }()
	stepContext, cancel := context.WithTimeout(ctx, step.Timeout)
	defer cancel()
	command := exec.Command(step.Executable, step.Arguments...)
	command.Dir = step.Directory
	command.Env = append([]string(nil), step.Environment...)
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return errors.New("finite profile command could not start")
	}
	supervised := childgroup.Supervise(stepContext, command, 2*time.Second)
	if supervised.Interrupted {
		return stepContext.Err()
	}
	if supervised.WaitErr != nil {
		return errors.New("finite profile command failed")
	}
	return nil
}

func validFiniteStep(step environmentcontrol.PreparationSpec) bool {
	for _, path := range []string{step.Executable, step.Directory, step.RunDirectory} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
			return false
		}
	}
	info, err := os.Lstat(step.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return false
	}
	if step.Timeout <= 0 || step.Timeout > 30*time.Minute {
		return false
	}
	required := map[string]bool{"HOME": false, "PATH": false, "TMPDIR": false}
	seen := make(map[string]struct{}, len(step.Environment))
	for _, entry := range step.Environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" || strings.ContainsRune(entry, 0) {
			return false
		}
		if _, duplicate := seen[name]; duplicate {
			return false
		}
		seen[name] = struct{}{}
		if _, tracked := required[name]; tracked {
			required[name] = true
		}
	}
	return required["HOME"] && required["PATH"] && required["TMPDIR"]
}

type boundedLog struct {
	file      *os.File
	remaining int64
}

func newBoundedLog(path string) (*boundedLog, error) {
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
		return nil, ErrProfileInvalid
	}
	return &boundedLog{file: file, remaining: maximumFiniteLogBytes}, nil
}

func (log *boundedLog) Write(contents []byte) (int, error) {
	original := len(contents)
	if log.remaining <= 0 {
		return original, nil
	}
	if int64(len(contents)) > log.remaining {
		contents = contents[:log.remaining]
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

var _ io.Writer = (*boundedLog)(nil)
