package domain

import (
	"errors"
	"testing"
)

func TestEnvironmentTransitions(t *testing.T) {
	tests := []struct {
		name    string
		from    EnvironmentState
		to      EnvironmentState
		wantErr bool
	}{
		{name: "discover stopped", from: EnvironmentUnknown, to: EnvironmentStopped},
		{name: "start", from: EnvironmentStopped, to: EnvironmentStarting},
		{name: "ready", from: EnvironmentStarting, to: EnvironmentRunning},
		{name: "stop", from: EnvironmentRunning, to: EnvironmentStopping},
		{name: "stopped", from: EnvironmentStopping, to: EnvironmentStopped},
		{name: "crash", from: EnvironmentRunning, to: EnvironmentFailed},
		{name: "worktree removed", from: EnvironmentRunning, to: EnvironmentOrphaned},
		{name: "external cleanup", from: EnvironmentOrphaned, to: EnvironmentStopped},
		{name: "idempotent observation", from: EnvironmentRunning, to: EnvironmentRunning},
		{name: "skip startup", from: EnvironmentStopped, to: EnvironmentRunning, wantErr: true},
		{name: "resurrect orphan", from: EnvironmentOrphaned, to: EnvironmentRunning, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateEnvironmentTransition(test.from, test.to)
			if test.wantErr {
				assertTransitionError(t, err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOperationTransitions(t *testing.T) {
	tests := []struct {
		name    string
		from    OperationState
		to      OperationState
		wantErr bool
	}{
		{name: "begin", from: OperationPending, to: OperationRunning},
		{name: "complete", from: OperationRunning, to: OperationSucceeded},
		{name: "fail before run", from: OperationPending, to: OperationFailed},
		{name: "cancel running", from: OperationRunning, to: OperationCancelled},
		{name: "terminal replay", from: OperationSucceeded, to: OperationSucceeded},
		{name: "restart terminal", from: OperationSucceeded, to: OperationRunning, wantErr: true},
		{name: "skip running", from: OperationPending, to: OperationSucceeded, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateOperationTransition(test.from, test.to)
			if test.wantErr {
				assertTransitionError(t, err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertTransitionError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected transition error")
	}
	var transitionError TransitionError
	if !errors.As(err, &transitionError) {
		t.Fatalf("got %T, want TransitionError", err)
	}
	if transitionError.Code() != InvalidTransitionCode {
		t.Fatalf("code: got %q, want %q", transitionError.Code(), InvalidTransitionCode)
	}
}
