package githubstatus

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	defaultMaximumStdout = 2 << 20
	defaultMaximumStderr = 64 << 10
)

var (
	ErrExecutableUnavailable = errors.New("github cli executable is unavailable")
	ErrCommandFailed         = errors.New("github cli command failed")
	ErrOutputTooLarge        = errors.New("github cli output exceeded its bound")
)

type Invocation struct {
	Executable string
	Arguments  []string
}

type Result struct {
	Stdout   []byte
	ExitCode int
}

type Runner interface {
	Run(context.Context, Invocation) (Result, error)
}

type OSRunner struct {
	Environment        []string
	MaximumStdoutBytes int
	MaximumStderrBytes int
}

func (runner OSRunner) Run(ctx context.Context, invocation Invocation) (Result, error) {
	if !filepath.IsAbs(invocation.Executable) {
		return Result{}, ErrExecutableUnavailable
	}
	maximumStdout := runner.MaximumStdoutBytes
	if maximumStdout <= 0 {
		maximumStdout = defaultMaximumStdout
	}
	maximumStderr := runner.MaximumStderrBytes
	if maximumStderr <= 0 {
		maximumStderr = defaultMaximumStderr
	}
	stdout := newBoundedBuffer(maximumStdout)
	stderr := newBoundedBuffer(maximumStderr)
	command := exec.CommandContext(ctx, invocation.Executable, invocation.Arguments...)
	command.Dir = "/"
	environment := runner.Environment
	if environment == nil {
		environment = os.Environ()
	}
	command.Env = sanitizedEnvironment(environment)
	command.Stdin = nil
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if stdout.overflow || stderr.overflow {
		return Result{}, ErrOutputTooLarge
	}
	if err == nil {
		return Result{Stdout: stdout.Bytes(), ExitCode: 0}, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return Result{Stdout: stdout.Bytes(), ExitCode: exitError.ExitCode()}, nil
	}
	return Result{}, ErrCommandFailed
}

func ResolveExecutable(configured string) (string, error) {
	candidates := []string{configured}
	if configured == "" {
		candidates = []string{"/opt/homebrew/bin/gh", "/usr/local/bin/gh"}
	}
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) || filepath.Clean(candidate) != candidate {
			continue
		}
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", ErrExecutableUnavailable
}

func sanitizedEnvironment(source []string) []string {
	allowed := map[string]struct{}{
		"HOME": {}, "USER": {}, "LOGNAME": {}, "TMPDIR": {},
		"LANG": {}, "LC_ALL": {}, "XDG_CONFIG_HOME": {},
		"HTTP_PROXY": {}, "HTTPS_PROXY": {}, "NO_PROXY": {},
		"http_proxy": {}, "https_proxy": {}, "no_proxy": {},
	}
	environment := make([]string, 0, len(allowed)+5)
	for _, entry := range source {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, keep := allowed[key]; keep {
			environment = append(environment, entry)
		}
	}
	environment = append(environment,
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
		"GH_PAGER=cat",
		"NO_COLOR=1",
		"CLICOLOR=0",
	)
	return environment
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit}
}

func (buffer *boundedBuffer) Write(contents []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		writeLength := min(remaining, len(contents))
		_, _ = buffer.buffer.Write(contents[:writeLength])
	}
	if len(contents) > remaining {
		buffer.overflow = true
	}
	return len(contents), nil
}

func (buffer *boundedBuffer) Bytes() []byte {
	return append([]byte(nil), buffer.buffer.Bytes()...)
}
