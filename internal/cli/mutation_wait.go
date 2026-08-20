package cli

import (
	"context"
	"errors"
	"time"

	"github.com/theronburger/switchyard/internal/apiclient"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/domain"
)

const (
	defaultPollInterval = 250 * time.Millisecond
	defaultWaitTimeout  = 5 * time.Minute
)

var (
	errMutationFailed   = errors.New("the Switchyard operation failed")
	errMutationCanceled = errors.New("the Switchyard operation was canceled")
)

func (a Application) waitForMutation(
	ctx context.Context,
	receipt contractv1.MutationReceipt,
	kind string,
	resourceID string,
	serviceIDs []string,
) error {
	timeout := a.WaitTimeout
	if timeout <= 0 {
		timeout = defaultWaitTimeout
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		snapshot, err := a.Backend.Status(waitContext)
		if err != nil {
			if retryableDaemonDiscoveryError(err) {
				if waitErr := a.waitForNextPoll(waitContext); waitErr == nil {
					continue
				}
				if errors.Is(waitContext.Err(), context.DeadlineExceeded) {
					return mutationWaitTimeout(receipt, kind)
				}
			}
			return err
		}
		operation, found := operationByID(snapshot, receipt.OperationID)
		if found {
			switch domain.OperationState(operation.State) {
			case domain.OperationFailed:
				return operationFailure(operation, errMutationFailed)
			case domain.OperationCancelled:
				return operationFailure(operation, errMutationCanceled)
			case domain.OperationSucceeded:
				if mutationVisible(snapshot, receipt, kind, resourceID, serviceIDs) {
					return nil
				}
			}
		}
		if err := a.waitForNextPoll(waitContext); err != nil {
			if errors.Is(waitContext.Err(), context.DeadlineExceeded) {
				return mutationWaitTimeout(receipt, kind)
			}
			return err
		}
	}
}

func mutationWaitTimeout(receipt contractv1.MutationReceipt, kind string) error {
	contractError := contractv1.ContractError{
		Code: "WAIT_TIMEOUT", Message: "Timed out waiting for the accepted Switchyard operation to finish.",
		Retryable: true, ResourceKind: "operation", ResourceID: receipt.OperationID,
		Phase: kind, Diagnostic: "The operation may still be running in the daemon.",
		NextAction: "inspect_operation_status",
	}
	return &apiclient.CodedError{Code: apiclient.ErrorWaitTimeout, Contract: &contractError}
}

func operationFailure(operation contractv1.Operation, fallback error) error {
	if operation.Error == nil || operation.Error.Code == "" {
		return fallback
	}
	contractError := *operation.Error
	return &apiclient.CodedError{
		Code: apiclient.ErrorCode(contractError.Code), Contract: &contractError,
	}
}

func (a Application) waitForNextPoll(ctx context.Context) error {
	interval := a.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func operationByID(snapshot contractv1.StatusSnapshot, operationID string) (contractv1.Operation, bool) {
	for _, operation := range snapshot.Operations {
		if operation.ID == operationID {
			return operation, true
		}
	}
	return contractv1.Operation{}, false
}

func mutationVisible(
	snapshot contractv1.StatusSnapshot,
	receipt contractv1.MutationReceipt,
	kind string,
	resourceID string,
	serviceIDs []string,
) bool {
	if kind == "action" {
		// A profile action's terminal operation state is its completion proof.
		return true
	}
	if kind == "prepare" {
		for _, repository := range snapshot.Repositories {
			for _, worktree := range repository.Worktrees {
				if worktree.ID == resourceID && worktree.Workspace != nil && worktree.Workspace.State == "ready" {
					return true
				}
			}
		}
		return false
	}
	for _, environment := range snapshot.Environments {
		if environment.ID != receipt.EnvironmentID {
			continue
		}
		if kind == "stop" {
			return environmentStopped(environment)
		}
		return environmentStarted(environment, receipt.RunID, serviceIDs)
	}
	return kind == "stop"
}

func environmentStopped(environment contractv1.Environment) bool {
	return environment.DesiredState == string(domain.EnvironmentStopped) &&
		environment.ObservedState == string(domain.EnvironmentStopped) &&
		len(environment.PortLeases) == 0 && len(environment.InfrastructureLeases) == 0
}

func environmentStarted(environment contractv1.Environment, runID string, serviceIDs []string) bool {
	if runID == "" || environment.DesiredState != string(domain.EnvironmentRunning) ||
		environment.ObservedState != string(domain.EnvironmentRunning) || environment.Health != "healthy" {
		return false
	}
	services := make(map[string]contractv1.Service, len(environment.Services))
	for _, service := range environment.Services {
		services[service.ID] = service
	}
	for _, serviceID := range serviceIDs {
		service, found := services[serviceID]
		if !found || service.ObservedState != string(domain.EnvironmentRunning) || service.Health != "healthy" ||
			service.Run == nil || service.Run.ID != runID {
			return false
		}
	}
	return true
}
