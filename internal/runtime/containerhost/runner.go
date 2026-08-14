package containerhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

const maximumCommandOutput = 64 * 1024 * 1024

type Command struct {
	Executable string
	Arguments  []string
}

func (command Command) Clone() Command {
	return Command{
		Executable: command.Executable,
		Arguments:  append([]string(nil), command.Arguments...),
	}
}

type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

type CommandError struct {
	Executable string
	ExitCode   int
	Started    bool
}

func (err *CommandError) Error() string {
	if !err.Started {
		return "container host command is unavailable"
	}
	return fmt.Sprintf("container host command failed with exit code %d", err.ExitCode)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, command Command) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if command.Executable == "" {
		return nil, errors.New("command executable is required")
	}

	stdout := &boundedBuffer{limit: maximumCommandOutput}
	process := exec.CommandContext(ctx, command.Executable, command.Arguments...)
	process.Stdout = stdout
	process.Stderr = io.Discard
	err := process.Run()
	if contextError := ctx.Err(); contextError != nil {
		return nil, contextError
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return nil, &CommandError{
				Executable: command.Executable,
				ExitCode:   exitError.ExitCode(),
				Started:    true,
			}
		}
		return nil, &CommandError{Executable: command.Executable, ExitCode: -1, Started: false}
	}
	if stdout.exceeded {
		return nil, errors.New("command output exceeded the safety limit")
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func redactRunnerError(command Command, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var commandError *CommandError
	if errors.As(err, &commandError) {
		return &CommandError{
			Executable: command.Executable,
			ExitCode:   commandError.ExitCode,
			Started:    commandError.Started,
		}
	}
	return &CommandError{Executable: command.Executable, ExitCode: -1, Started: true}
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(contents []byte) (int, error) {
	originalLength := len(contents)
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = true
		return originalLength, nil
	}
	if len(contents) > remaining {
		contents = contents[:remaining]
		buffer.exceeded = true
	}
	_, _ = buffer.Buffer.Write(contents)
	return originalLength, nil
}
