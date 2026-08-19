package environment

import (
	"context"
	"errors"

	"github.com/theronburger/switchyard/internal/domain"
)

func (coordinator *Coordinator) Reconcile(ctx context.Context) ([]ReconcileOutcome, error) {
	operations, err := coordinator.journal.Incomplete(ctx)
	if err != nil {
		return nil, err
	}
	outcomes := make([]ReconcileOutcome, 0, len(operations))
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return outcomes, err
		}
		lock := coordinator.lockFor(operation.EnvironmentID)
		lock.Lock()
		outcome := coordinator.reconcileOperation(ctx, operation)
		lock.Unlock()
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func (coordinator *Coordinator) reconcileOperation(
	ctx context.Context,
	operation OperationRecord,
) ReconcileOutcome {
	outcome := ReconcileOutcome{OperationID: operation.ID, EnvironmentID: operation.EnvironmentID}
	if operation.ID == "" || operation.EnvironmentID == "" || operation.RunID == "" ||
		(operation.State != domain.OperationPending && operation.State != domain.OperationRunning) {
		outcome.Err = ErrInvalidRequest
		return outcome
	}
	switch operation.Kind {
	case OperationStart:
		result, err := coordinator.reconcileStart(ctx, operation)
		outcome.State = result.State
		outcome.Err = err
	case OperationStop:
		result, err := coordinator.reconcileStop(ctx, operation)
		outcome.State = result.State
		outcome.Err = err
	default:
		outcome.Err = ErrInvalidRequest
	}
	return outcome
}

func (coordinator *Coordinator) reconcileStart(
	ctx context.Context,
	operation OperationRecord,
) (EnvironmentResult, error) {
	if operation.EnvironmentState == domain.EnvironmentStarting ||
		operation.EnvironmentState == domain.EnvironmentRunning ||
		operation.EnvironmentState == domain.EnvironmentFailed {
		if err := transitionEnvironment(&operation, domain.EnvironmentStopping); err != nil {
			return EnvironmentResult{}, err
		}
	}
	operation.Phase = PhaseRollingBack
	if err := transitionOperation(&operation, domain.OperationRunning); err != nil {
		return EnvironmentResult{}, err
	}
	if err := coordinator.journal.Update(ctx, operation); err != nil {
		return EnvironmentResult{}, err
	}
	rollbackError := coordinator.rollbackStart(ctx, &operation)
	result := environmentFromRollback(operation)
	if rollbackError == nil {
		if operation.EnvironmentState == domain.EnvironmentUnknown {
			if err := transitionEnvironment(&operation, domain.EnvironmentStopped); err != nil {
				return EnvironmentResult{}, err
			}
		} else if operation.EnvironmentState != domain.EnvironmentStopped {
			if err := transitionEnvironment(&operation, domain.EnvironmentStopped); err != nil {
				return EnvironmentResult{}, err
			}
		}
		result = EnvironmentResult{
			EnvironmentID: operation.EnvironmentID, RunID: operation.RunID,
			TargetID: operationTargetID(operation), State: domain.EnvironmentStopped,
			Source:    cloneSource(operation.Source),
			UpdatedAt: coordinator.now().UTC(),
		}
	} else {
		if operation.EnvironmentState != domain.EnvironmentFailed {
			if err := transitionEnvironment(&operation, domain.EnvironmentFailed); err != nil {
				return EnvironmentResult{}, errors.Join(rollbackError, err)
			}
		}
		result.State = domain.EnvironmentFailed
		result.UpdatedAt = coordinator.now().UTC()
	}
	if err := transitionOperation(&operation, domain.OperationFailed); err != nil {
		return result, errors.Join(rollbackError, err)
	}
	operation.Phase = PhaseComplete
	operation.Failure = "environment start was interrupted by daemon restart"
	if err := coordinator.journal.Publish(ctx, operation, cloneEnvironment(result)); err != nil {
		return result, errors.Join(rollbackError, err)
	}
	return result, rollbackError
}

func (coordinator *Coordinator) reconcileStop(
	ctx context.Context,
	operation OperationRecord,
) (EnvironmentResult, error) {
	if operation.Target == nil {
		return EnvironmentResult{}, ErrInvalidRequest
	}
	target := cloneEnvironment(*operation.Target)
	if err := coordinator.validateOwnedResult(target); err != nil {
		return coordinator.failStop(operation, target, err)
	}
	if err := coordinator.requireStopDependencies(target); err != nil {
		return coordinator.failStop(operation, target, err)
	}
	if operation.EnvironmentState != domain.EnvironmentStopped &&
		operation.EnvironmentState != domain.EnvironmentStopping {
		if err := transitionEnvironment(&operation, domain.EnvironmentStopping); err != nil {
			return EnvironmentResult{}, err
		}
	}
	if err := transitionOperation(&operation, domain.OperationRunning); err != nil {
		return EnvironmentResult{}, err
	}
	if err := coordinator.journal.Update(ctx, operation); err != nil {
		return EnvironmentResult{}, err
	}
	if err := coordinator.stopTarget(ctx, &operation, target); err != nil {
		return coordinator.failStop(operation, target, err)
	}
	return coordinator.publishStopped(ctx, operation, target)
}

func environmentFromRollback(operation OperationRecord) EnvironmentResult {
	targetID := operationTargetID(operation)
	result := EnvironmentResult{
		EnvironmentID: operation.EnvironmentID,
		RunID:         operation.RunID,
		TargetID:      targetID,
		State:         operation.EnvironmentState,
		Source:        cloneSource(operation.Source),
	}
	for _, entry := range operation.Rollback {
		if !entry.Armed {
			continue
		}
		switch entry.Kind {
		case RollbackPorts:
			result.Ports = append(result.Ports, entry.Leases...)
		case RollbackProjection:
			result.Projection = cloneProjection(entry.Projection)
		case RollbackInfrastructure:
			result.Infrastructure = append(result.Infrastructure, entry.Infrastructure...)
		case RollbackProcess:
			if entry.Process != nil {
				result.Services = append(result.Services, *cloneService(entry.Process))
			}
		}
	}
	return result
}

func operationTargetID(operation OperationRecord) string {
	if operation.Intent == nil {
		return ""
	}
	return operation.Intent.TargetID
}
