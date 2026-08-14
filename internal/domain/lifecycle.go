package domain

import "fmt"

const InvalidTransitionCode = "INVALID_TRANSITION"

type EnvironmentState string

const (
	EnvironmentUnknown  EnvironmentState = "unknown"
	EnvironmentStopped  EnvironmentState = "stopped"
	EnvironmentStarting EnvironmentState = "starting"
	EnvironmentRunning  EnvironmentState = "running"
	EnvironmentStopping EnvironmentState = "stopping"
	EnvironmentFailed   EnvironmentState = "failed"
	EnvironmentOrphaned EnvironmentState = "orphaned"
)

type OperationState string

const (
	OperationPending   OperationState = "pending"
	OperationRunning   OperationState = "running"
	OperationSucceeded OperationState = "succeeded"
	OperationFailed    OperationState = "failed"
	OperationCancelled OperationState = "cancelled"
)

type TransitionError struct {
	ResourceKind string
	From         string
	To           string
}

func (transition TransitionError) Error() string {
	return fmt.Sprintf("%s cannot transition from %q to %q", transition.ResourceKind, transition.From, transition.To)
}

func (TransitionError) Code() string {
	return InvalidTransitionCode
}

func ValidateEnvironmentTransition(from, to EnvironmentState) error {
	if from == to {
		return nil
	}

	allowed := map[EnvironmentState]map[EnvironmentState]bool{
		EnvironmentUnknown: {
			EnvironmentStopped:  true,
			EnvironmentStarting: true,
			EnvironmentFailed:   true,
			EnvironmentOrphaned: true,
		},
		EnvironmentStopped: {
			EnvironmentStarting: true,
			EnvironmentOrphaned: true,
		},
		EnvironmentStarting: {
			EnvironmentRunning:  true,
			EnvironmentStopping: true,
			EnvironmentFailed:   true,
			EnvironmentOrphaned: true,
		},
		EnvironmentRunning: {
			EnvironmentStopping: true,
			EnvironmentFailed:   true,
			EnvironmentOrphaned: true,
		},
		EnvironmentStopping: {
			EnvironmentStopped:  true,
			EnvironmentFailed:   true,
			EnvironmentOrphaned: true,
		},
		EnvironmentFailed: {
			EnvironmentStarting: true,
			EnvironmentStopping: true,
			EnvironmentStopped:  true,
			EnvironmentOrphaned: true,
		},
		EnvironmentOrphaned: {
			EnvironmentStopped: true,
		},
	}

	if allowed[from][to] {
		return nil
	}

	return TransitionError{
		ResourceKind: "environment",
		From:         string(from),
		To:           string(to),
	}
}

func ValidateOperationTransition(from, to OperationState) error {
	if from == to {
		return nil
	}

	allowed := map[OperationState]map[OperationState]bool{
		OperationPending: {
			OperationRunning:   true,
			OperationFailed:    true,
			OperationCancelled: true,
		},
		OperationRunning: {
			OperationSucceeded: true,
			OperationFailed:    true,
			OperationCancelled: true,
		},
	}

	if allowed[from][to] {
		return nil
	}

	return TransitionError{
		ResourceKind: "operation",
		From:         string(from),
		To:           string(to),
	}
}
