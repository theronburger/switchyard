package marketplace

import (
	"context"
	"errors"
)

type recordingRunner struct {
	outputs     []CommandOutput
	err         error
	invocations []Invocation
}

func (runner *recordingRunner) Run(_ context.Context, invocation Invocation) (CommandOutput, error) {
	runner.invocations = append(runner.invocations, cloneInvocation(invocation))
	if runner.err != nil {
		return CommandOutput{}, runner.err
	}
	if len(runner.outputs) == 0 {
		return CommandOutput{}, errors.New("recording runner has no output")
	}
	output := runner.outputs[0]
	runner.outputs = runner.outputs[1:]
	return output, nil
}

func cloneInvocation(invocation Invocation) Invocation {
	invocation.Arguments = append([]string(nil), invocation.Arguments...)
	return invocation
}
