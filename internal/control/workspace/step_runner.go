package workspace

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
)

const maximumStepLogBytes = 4 * 1024 * 1024

type ExactStepRunner struct {
	RuntimeRoot string
	GracePeriod time.Duration
}

func (runner ExactStepRunner) Run(ctx context.Context, step StepSpec) error {
	if ctx == nil || ctx.Err() != nil || !validStep(step) ||
		!pathContained(runner.RuntimeRoot, step.RunDirectory) {
		return ErrInvalidPlan
	}
	if err := createPrivateDirectoryTree(runner.RuntimeRoot, step.RunDirectory); err != nil {
		return ErrInvalidPlan
	}
	stdout, err := openBoundedLog(filepath.Join(step.RunDirectory, "stdout.log"))
	if err != nil {
		return ErrInvalidPlan
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := openBoundedLog(filepath.Join(step.RunDirectory, "stderr.log"))
	if err != nil {
		return ErrInvalidPlan
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
		return ErrStepFailed
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case err := <-waited:
		if err != nil {
			return ErrStepFailed
		}
		return nil
	case <-stepContext.Done():
	}
	select {
	case err := <-waited:
		if err != nil {
			return errors.Join(stepContext.Err(), ErrStepFailed)
		}
		return stepContext.Err()
	default:
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	grace := runner.GracePeriod
	if grace <= 0 {
		grace = 2 * time.Second
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-waited:
		return stepContext.Err()
	case <-timer.C:
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-waited
		return stepContext.Err()
	}
}

func pathContained(root, path string) bool {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func createPrivateDirectoryTree(root, destination string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidPlan
	}
	relative, err := filepath.Rel(root, destination)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidPlan
		}
		if err := os.Chmod(current, 0o700); err != nil {
			return err
		}
	}
	return nil
}

type boundedLog struct {
	file      *os.File
	remaining int64
}

func openBoundedLog(path string) (*boundedLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		return nil, ErrInvalidPlan
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		_ = file.Close()
		return nil, ErrInvalidPlan
	}
	return &boundedLog{file: file, remaining: maximumStepLogBytes}, nil
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
