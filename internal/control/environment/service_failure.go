package environment

import (
	"context"
	"errors"
	"path"
)

// Failure codes for service-scoped environment start failures. Each one
// carries the owned log reference for the affected service or preparation
// so the diagnostics read promised by NextAction can resolve it.
const (
	FailureCodeServiceReadinessTimedOut = "ENVIRONMENT_SERVICE_READINESS_TIMED_OUT"
	FailureCodeServiceReadinessFailed   = "ENVIRONMENT_SERVICE_READINESS_FAILED"
	FailureCodeServiceHealthFailed      = "ENVIRONMENT_SERVICE_HEALTH_CHECK_FAILED"
	FailureCodeServiceExited            = "ENVIRONMENT_SERVICE_EXITED_AFTER_READINESS"
	FailureCodePreparationTimedOut      = "ENVIRONMENT_PREPARATION_TIMED_OUT"
	FailureCodePreparationFailed        = "ENVIRONMENT_PREPARATION_FAILED"

	nextActionInspectDiagnostics = "inspect_operation_diagnostics"
)

// serviceFailure wraps the private cause of one service-scoped start failure
// with the bounded structured failure the operation persists. The cause stays
// reachable through errors.Is/errors.As for callers and tests; only the
// structured failure crosses the journal boundary.
type serviceFailure struct {
	cause   error
	failure OperationFailure
}

func (failure *serviceFailure) Error() string { return failure.cause.Error() }

func (failure *serviceFailure) Unwrap() error { return failure.cause }

func (failure *serviceFailure) OperationFailure() OperationFailure { return failure.failure }

// ServiceLogReference is the run-relative, slash-separated reference to the
// owned stdout/stderr directory of one launched service. It is resolved by
// the daemon beneath the environment's run root and never names an absolute
// path.
func ServiceLogReference(runID, serviceID string) string {
	return path.Join(runID, "services", serviceID)
}

// attributed leaves cancellation from the caller's own context and already
// structured failures untouched, so a daemon shutdown is still recorded as a
// cancellation and a runner-provided failure keeps its own detail.
func attributed(ctx context.Context, cause error) (error, bool) {
	var provider OperationFailureProvider
	if cause == nil || ctx.Err() != nil || errors.As(cause, &provider) {
		return cause, false
	}
	return cause, true
}

// readinessFailure attributes a readiness wait error to the service whose
// probes did not pass, referencing that service's owned logs.
func readinessFailure(ctx context.Context, runID string, service ServiceLaunch, cause error) error {
	cause, attribute := attributed(ctx, cause)
	if !attribute {
		return cause
	}
	failure := OperationFailure{
		Code:    FailureCodeServiceReadinessFailed,
		Message: "Service readiness could not be established; its owned logs are available through operation diagnostics.",
		Diagnostic: "A readiness probe for the service returned an error before the service became ready. " +
			"Inspect the service's stdout and stderr excerpts for the startup failure.",
	}
	if errors.Is(cause, ErrReadinessTimedOut) {
		failure.Code = FailureCodeServiceReadinessTimedOut
		failure.Message = "Service did not become ready within its readiness timeout; its owned logs are available through operation diagnostics."
		failure.Diagnostic = "Every readiness probe for the service was still failing when the configured readiness timeout elapsed. " +
			"Inspect the service's stdout and stderr excerpts for a bind, crash, or slow-start cause."
	}
	return &serviceFailure{cause: cause, failure: serviceScoped(failure, runID, service.ID, service.Readiness.ID)}
}

// healthFailure attributes a post-readiness health check error to its service.
func healthFailure(ctx context.Context, runID string, service ServiceLaunch, cause error) error {
	cause, attribute := attributed(ctx, cause)
	if !attribute {
		return cause
	}
	failure := OperationFailure{
		Code:    FailureCodeServiceHealthFailed,
		Message: "Service health check failed after readiness; its owned logs are available through operation diagnostics.",
		Diagnostic: "A health probe for the service returned an error after readiness passed. " +
			"Inspect the service's stdout and stderr excerpts for the failure.",
	}
	return &serviceFailure{cause: cause, failure: serviceScoped(failure, runID, service.ID, service.Readiness.ID)}
}

// exitedFailure records that an owned service process was no longer running
// once readiness and health had passed.
func exitedFailure(ctx context.Context, service ServiceResult) error {
	cause, attribute := attributed(ctx, ErrProcessNotRunning)
	if !attribute {
		return cause
	}
	failure := OperationFailure{
		Code:    FailureCodeServiceExited,
		Message: "Service process exited after readiness; its owned logs are available through operation diagnostics.",
		Diagnostic: "The service's owned process group was not running when Switchyard verified it after readiness and health passed. " +
			"Inspect the service's stdout and stderr excerpts for the exit cause.",
	}
	return &serviceFailure{cause: cause, failure: serviceScoped(failure, service.RunID, service.ID, service.Readiness.ID)}
}

// preparationFailure attributes a finite preparation or initialization
// command error to its step, referencing the step's owned logs.
func preparationFailure(ctx context.Context, step PreparationSpec, cause error) error {
	cause, attribute := attributed(ctx, cause)
	if !attribute {
		return cause
	}
	failure := OperationFailure{
		Code:    FailureCodePreparationFailed,
		Message: "Preparation command failed; its owned logs are available through operation diagnostics.",
		Diagnostic: "A finite preparation command did not complete successfully. " +
			"Inspect the command's stdout and stderr excerpts for the failure.",
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		failure.Code = FailureCodePreparationTimedOut
		failure.Message = "Preparation command timed out; its owned logs are available through operation diagnostics."
		failure.Diagnostic = "A finite preparation command was still running when its configured timeout elapsed and was stopped. " +
			"Inspect the command's stdout and stderr excerpts for what it was waiting on."
	}
	failure = serviceScoped(failure, "", step.ServiceID, step.ID)
	failure.LogReference = step.LogReference
	if failure.LogReference == "" {
		failure.NextAction = "retry"
	}
	return &serviceFailure{cause: cause, failure: failure}
}

func serviceScoped(failure OperationFailure, runID, serviceID, step string) OperationFailure {
	failure.Retryable = true
	failure.ResourceKind = "service"
	failure.ResourceID = serviceID
	failure.Step = step
	failure.NextAction = nextActionInspectDiagnostics
	if runID != "" && serviceID != "" {
		failure.LogReference = ServiceLogReference(runID, serviceID)
	}
	return failure
}
