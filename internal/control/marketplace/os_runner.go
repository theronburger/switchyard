package marketplacecontrol

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
)

const maximumCommandOutput = 4 * 1024 * 1024

type OSCommandRunner struct{}

func (OSCommandRunner) Run(
	ctx context.Context,
	invocation marketplaceadapter.Invocation,
) (marketplaceadapter.CommandOutput, error) {
	if err := ctx.Err(); err != nil {
		return marketplaceadapter.CommandOutput{}, err
	}
	if invocation.Executable == "" {
		return marketplaceadapter.CommandOutput{}, errors.New("repository command is unavailable")
	}

	stdout := &boundedOutput{limit: maximumCommandOutput}
	command := exec.CommandContext(ctx, invocation.Executable, invocation.Arguments...)
	command.Dir = invocation.WorkingDirectory
	command.Stdout = stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return marketplaceadapter.CommandOutput{}, contextError
		}
		return marketplaceadapter.CommandOutput{}, errors.New("repository command failed")
	}
	if stdout.exceeded {
		return marketplaceadapter.CommandOutput{}, errors.New("repository command output exceeded its limit")
	}
	return marketplaceadapter.CommandOutput{Stdout: append([]byte(nil), stdout.Bytes()...)}, nil
}

type boundedOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (output *boundedOutput) Write(contents []byte) (int, error) {
	originalLength := len(contents)
	remaining := output.limit - output.Len()
	if remaining <= 0 {
		output.exceeded = true
		return originalLength, nil
	}
	if len(contents) > remaining {
		contents = contents[:remaining]
		output.exceeded = true
	}
	_, _ = output.Buffer.Write(contents)
	return originalLength, nil
}
