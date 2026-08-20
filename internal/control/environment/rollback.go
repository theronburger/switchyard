package environment

import (
	"context"
	"errors"
	"os"

	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

func (coordinator *Coordinator) failStart(
	operation OperationRecord,
	result EnvironmentResult,
	cause error,
) (EnvironmentResult, error) {
	failureDetail := safeFailureDetail(cause, operation.Phase)
	cleanupContext, cancel := context.WithTimeout(context.Background(), coordinator.rollbackTimeout)
	defer cancel()

	var persistenceError error
	if operation.EnvironmentState != domain.EnvironmentStopping {
		if err := transitionEnvironment(&operation, domain.EnvironmentStopping); err != nil {
			persistenceError = errors.Join(persistenceError, err)
		}
	}
	operation.Phase = PhaseRollingBack
	if err := coordinator.journal.Update(cleanupContext, operation); err != nil {
		persistenceError = errors.Join(persistenceError, err)
	}
	rollbackError := coordinator.rollbackStart(cleanupContext, &operation)

	if rollbackError == nil {
		if err := transitionEnvironment(&operation, domain.EnvironmentStopped); err != nil {
			persistenceError = errors.Join(persistenceError, err)
		}
		result.Ports = leasesNotOwnedByRollback(result.Ports, operation.Rollback)
		result.Projection = nil
		result.Infrastructure = nil
		result.Services = nil
	} else {
		if err := transitionEnvironment(&operation, domain.EnvironmentFailed); err != nil {
			persistenceError = errors.Join(persistenceError, err)
		}
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		if operation.State == domain.OperationSucceeded {
			// The succeeded state was only offered to an atomic Publish that
			// failed; the durable operation is still running.
			operation.State = domain.OperationRunning
		}
		if err := transitionOperation(&operation, domain.OperationCancelled); err != nil {
			persistenceError = errors.Join(persistenceError, err)
		}
	} else {
		if operation.State == domain.OperationSucceeded {
			operation.State = domain.OperationRunning
		}
		if err := transitionOperation(&operation, domain.OperationFailed); err != nil {
			persistenceError = errors.Join(persistenceError, err)
		}
	}
	operation.Phase = PhaseComplete
	operation.Failure = safeFailure(cause)
	operation.FailureDetail = &failureDetail
	result.State = operation.EnvironmentState
	result.UpdatedAt = coordinator.now().UTC()
	if err := coordinator.journal.Publish(cleanupContext, operation, cloneEnvironment(result)); err != nil {
		persistenceError = errors.Join(persistenceError, err)
	}
	return result, errors.Join(cause, rollbackError, persistenceError)
}

func (coordinator *Coordinator) rollbackStart(ctx context.Context, operation *OperationRecord) error {
	var rollbackError error
	for index := len(operation.Rollback) - 1; index >= 0; index-- {
		if !operation.Rollback[index].Armed {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(rollbackError, err)
		}
		entry := operation.Rollback[index]
		if err := coordinator.rollbackEntry(ctx, operation.EnvironmentID, operation.RunID, entry); err != nil {
			rollbackError = errors.Join(rollbackError, err)
			continue
		}
		operation.Rollback[index].Armed = false
		if err := coordinator.journal.Update(ctx, *operation); err != nil {
			rollbackError = errors.Join(rollbackError, err)
		}
	}
	return rollbackError
}

func (coordinator *Coordinator) rollbackEntry(
	ctx context.Context,
	environmentID string,
	runID string,
	entry RollbackEntry,
) error {
	if err := validateRollbackEntry(environmentID, runID, entry); err != nil {
		return err
	}
	switch entry.Kind {
	case RollbackPorts:
		for _, key := range entry.PortKeys {
			coordinator.ports.Release(key)
		}
		return nil
	case RollbackProjection:
		return coordinator.projections.Rollback(ctx, *entry.Projection)
	case RollbackInfrastructure:
		return coordinator.infrastructure.StopOwned(ctx, cloneGoals(entry.Infrastructure))
	case RollbackProcess:
		_, err := coordinator.processes.Stop(ctx, entry.Process.OwnershipPath)
		if errors.Is(err, processhost.ErrOrphanUnverified) {
			return err
		}
		if errors.Is(err, os.ErrNotExist) && !entry.Applied {
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return processhost.ErrOrphanUnverified
		}
		return err
	default:
		return ErrInvalidRequest
	}
}

func (coordinator *Coordinator) stopTarget(
	ctx context.Context,
	operation *OperationRecord,
	target EnvironmentResult,
) error {
	if len(target.Services) != 0 {
		if err := coordinator.checkpoint(ctx, operation, PhaseStoppingServices); err != nil {
			return err
		}
		for index := len(target.Services) - 1; index >= 0; index-- {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := coordinator.processes.Stop(ctx, target.Services[index].OwnershipPath)
			if err != nil {
				return err
			}
		}
	}
	if len(target.Infrastructure) != 0 {
		if err := coordinator.checkpoint(ctx, operation, PhaseStoppingInfrastructure); err != nil {
			return err
		}
		if err := coordinator.infrastructure.StopOwned(ctx, cloneGoals(target.Infrastructure)); err != nil {
			return err
		}
	}
	if target.Projection != nil {
		if err := coordinator.checkpoint(ctx, operation, PhaseRemovingProjection); err != nil {
			return err
		}
		if err := coordinator.projections.Rollback(ctx, *cloneProjection(target.Projection)); err != nil {
			return err
		}
	}
	if len(target.Ports) != 0 {
		if err := coordinator.checkpoint(ctx, operation, PhaseReleasingPorts); err != nil {
			return err
		}
		for _, lease := range target.Ports {
			coordinator.ports.Release(lease.Key)
		}
	}
	return nil
}

func (coordinator *Coordinator) publishStopped(
	ctx context.Context,
	operation OperationRecord,
	previous EnvironmentResult,
) (EnvironmentResult, error) {
	if operation.EnvironmentState != domain.EnvironmentStopped {
		if err := transitionEnvironment(&operation, domain.EnvironmentStopped); err != nil {
			return EnvironmentResult{}, err
		}
	}
	if err := transitionOperation(&operation, domain.OperationSucceeded); err != nil {
		return EnvironmentResult{}, err
	}
	operation.Phase = PhaseComplete
	stopped := EnvironmentResult{
		EnvironmentID: previous.EnvironmentID,
		RunID:         previous.RunID,
		TargetID:      previous.TargetID,
		ProfileDigest: previous.ProfileDigest,
		Source:        cloneSource(previous.Source),
		State:         domain.EnvironmentStopped,
		UpdatedAt:     coordinator.now().UTC(),
	}
	if err := coordinator.journal.Publish(ctx, operation, stopped); err != nil {
		return EnvironmentResult{}, err
	}
	return stopped, nil
}

func (coordinator *Coordinator) failStop(
	operation OperationRecord,
	current EnvironmentResult,
	cause error,
) (EnvironmentResult, error) {
	failureDetail := safeFailureDetail(cause, operation.Phase)
	cleanupContext, cancel := context.WithTimeout(context.Background(), coordinator.rollbackTimeout)
	defer cancel()
	if operation.EnvironmentState != domain.EnvironmentFailed {
		_ = transitionEnvironment(&operation, domain.EnvironmentFailed)
	}
	var transitionError error
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		transitionError = transitionOperation(&operation, domain.OperationCancelled)
	} else {
		transitionError = transitionOperation(&operation, domain.OperationFailed)
	}
	operation.Phase = PhaseComplete
	operation.Failure = safeFailure(cause)
	operation.FailureDetail = &failureDetail
	current.State = domain.EnvironmentFailed
	current.UpdatedAt = coordinator.now().UTC()
	publishError := coordinator.journal.Publish(cleanupContext, operation, cloneEnvironment(current))
	return current, errors.Join(cause, transitionError, publishError)
}

func safeFailure(err error) string {
	if err == nil {
		return "environment operation failed"
	}
	if errors.Is(err, processhost.ErrOwnershipMismatch) || errors.Is(err, processhost.ErrOrphanUnverified) {
		return OperationFailureOwnershipUnverified
	}
	if errors.Is(err, context.Canceled) {
		return "environment operation was cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "environment operation timed out"
	}
	return "environment operation failed"
}

func safeFailureDetail(err error, phase OperationPhase) OperationFailure {
	if phase == PhaseComplete {
		phase = PhasePublishingResult
	}
	var provider OperationFailureProvider
	if errors.As(err, &provider) {
		failure := provider.OperationFailure()
		failure.Phase = phase
		return failure
	}
	if errors.Is(err, processhost.ErrOwnershipMismatch) || errors.Is(err, processhost.ErrOrphanUnverified) {
		return OperationFailure{
			Code:      "ENVIRONMENT_PROCESS_OWNERSHIP_UNVERIFIED",
			Message:   "Switchyard could not verify ownership of one or more service processes, so it did not signal them.",
			Retryable: true, Phase: phase, ResourceKind: "environment", NextAction: "repair_process_ownership",
		}
	}
	if errors.Is(err, context.Canceled) {
		return OperationFailure{
			Code: "ENVIRONMENT_OPERATION_CANCELLED", Message: "Environment operation was cancelled.",
			Retryable: true, Phase: phase, ResourceKind: "environment", NextAction: "retry",
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return OperationFailure{
			Code: "ENVIRONMENT_OPERATION_TIMED_OUT", Message: "Environment operation timed out.",
			Retryable: true, Phase: phase, ResourceKind: "environment", NextAction: "inspect_operation_diagnostics",
		}
	}
	return OperationFailure{
		Code: "ENVIRONMENT_OPERATION_FAILED", Message: "Environment operation failed.",
		Retryable: true, Phase: phase, ResourceKind: "environment", NextAction: "inspect_operation_diagnostics",
	}
}
