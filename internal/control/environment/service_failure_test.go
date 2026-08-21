package environment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

// A service that never becomes ready must leave behind a failed operation
// whose structured failure names the service and references its owned run
// logs, so the diagnostics read promised by NextAction can actually resolve.
func TestReadinessTimeoutPersistsServiceLogReference(t *testing.T) {
	journal := newMemoryJournal()
	cause := errors.Join(ErrReadinessTimedOut, errors.New("probe http://127.0.0.1:43121/ready for person@example.com under /Users/person"))
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7300, nil),
		Planner:     &staticPlanner{plan: fullExecutionPlan(t, "env_timeout", "run_timeout")},
		Projections: &fakeProjection{}, Infrastructure: &fakeInfrastructure{}, Processes: &fakeProcesses{},
		Readiness: &fakeReadiness{err: cause}, RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Start(context.Background(), fullStartRequest(t, "op_timeout", "env_timeout", "run_timeout"))
	if !errors.Is(err, ErrReadinessTimedOut) {
		t.Fatalf("start error: %v", err)
	}
	operation := journal.operation("op_timeout")
	if operation.State != domain.OperationFailed || operation.FailureDetail == nil {
		t.Fatalf("timed-out start was not persisted as failed with detail: %+v", operation)
	}
	failure := *operation.FailureDetail
	if failure.Code != FailureCodeServiceReadinessTimedOut || failure.Phase != PhaseWaitingReadiness ||
		failure.ResourceKind != "service" || failure.ResourceID != "service_web" || failure.Step != "http" ||
		failure.LogReference != "run_timeout/services/service_web" || failure.NextAction != "inspect_operation_diagnostics" ||
		!failure.Retryable || failure.Message == "" || failure.Diagnostic == "" {
		t.Fatalf("readiness timeout failure: %+v", failure)
	}
	for _, text := range []string{failure.Message, failure.Diagnostic, operation.Failure, failure.LogReference} {
		for _, forbidden := range []string{"example.com", "/Users/", "43121", "http://"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("persisted failure leaked %q: %q", forbidden, text)
			}
		}
	}
	if strings.HasPrefix(failure.LogReference, "/") || strings.Contains(failure.LogReference, "..") || len(failure.LogReference) > 1024 {
		t.Fatalf("log reference is not a bounded relative reference: %q", failure.LogReference)
	}
}

func TestReadinessProbeErrorPersistsServiceLogReference(t *testing.T) {
	journal := newMemoryJournal()
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7310, nil),
		Planner:     &staticPlanner{plan: fullExecutionPlan(t, "env_probe", "run_probe")},
		Projections: &fakeProjection{}, Infrastructure: &fakeInfrastructure{}, Processes: &fakeProcesses{},
		Readiness: &fakeReadiness{err: errors.New("dial tcp: connection refused")}, RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = coordinator.Start(context.Background(), fullStartRequest(t, "op_probe", "env_probe", "run_probe"))
	operation := journal.operation("op_probe")
	if operation.State != domain.OperationFailed || operation.FailureDetail == nil ||
		operation.FailureDetail.Code != FailureCodeServiceReadinessFailed ||
		operation.FailureDetail.LogReference != "run_probe/services/service_web" ||
		operation.FailureDetail.ResourceID != "service_web" ||
		strings.Contains(operation.FailureDetail.Diagnostic, "connection refused") {
		t.Fatalf("readiness failure: state=%s detail=%+v", operation.State, operation.FailureDetail)
	}
}

// A caller cancellation during readiness stays a cancellation: it is not
// attributed to the service, because the service did nothing wrong.
func TestReadinessCancellationIsNotAttributedToTheService(t *testing.T) {
	journal := newMemoryJournal()
	readinessEntered := make(chan struct{}, 1)
	readinessBlock := make(chan struct{})
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7320, nil),
		Planner:     &staticPlanner{plan: fullExecutionPlan(t, "env_cancel", "run_cancel")},
		Projections: &fakeProjection{}, Infrastructure: &fakeInfrastructure{}, Processes: &fakeProcesses{},
		Readiness:       &fakeReadiness{entered: readinessEntered, block: readinessBlock},
		RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := coordinator.Start(ctx, fullStartRequest(t, "op_cancel", "env_cancel", "run_cancel"))
		done <- err
	}()
	select {
	case <-readinessEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness was not reached")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled start error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled start did not return")
	}
	operation := journal.operation("op_cancel")
	if operation.State != domain.OperationCancelled || operation.FailureDetail == nil ||
		operation.FailureDetail.Code != "ENVIRONMENT_OPERATION_CANCELLED" || operation.FailureDetail.LogReference != "" {
		t.Fatalf("cancelled readiness: state=%s detail=%+v", operation.State, operation.FailureDetail)
	}
}

// A health probe that errors after readiness passed is attributed to the
// service that failed it, with the probe identity and owned logs.
func TestHealthCheckFailurePersistsServiceLogReference(t *testing.T) {
	journal := newMemoryJournal()
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7350, nil),
		Planner:     &staticPlanner{plan: fullExecutionPlan(t, "env_health", "run_health")},
		Projections: &fakeProjection{}, Infrastructure: &fakeInfrastructure{}, Processes: &fakeProcesses{},
		Readiness:       &fakeReadiness{healthErr: errors.New("GET http://127.0.0.1:43121/health: 503 for person@example.com")},
		RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Start(context.Background(), fullStartRequest(t, "op_health", "env_health", "run_health"))
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("start error: %v", err)
	}
	operation := journal.operation("op_health")
	if operation.State != domain.OperationFailed || operation.FailureDetail == nil {
		t.Fatalf("health failure was not persisted as failed with detail: %+v", operation)
	}
	failure := *operation.FailureDetail
	if failure.Code != FailureCodeServiceHealthFailed || failure.Phase != PhaseWaitingReadiness ||
		failure.ResourceKind != "service" || failure.ResourceID != "service_web" || failure.Step != "http" ||
		failure.LogReference != "run_health/services/service_web" || failure.NextAction != "inspect_operation_diagnostics" ||
		!failure.Retryable {
		t.Fatalf("health check failure: %+v", failure)
	}
	for _, text := range []string{failure.Message, failure.Diagnostic, operation.Failure} {
		for _, forbidden := range []string{"example.com", "43121", "http://", "503"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("persisted health failure leaked %q: %q", forbidden, text)
			}
		}
	}
}

func TestServiceExitAfterReadinessPersistsServiceLogReference(t *testing.T) {
	journal := newMemoryJournal()
	processes := &fakeProcesses{reconcileObservation: processhost.Observation{State: "exited", OwnershipVerified: true}}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7330, nil),
		Planner:     &staticPlanner{plan: fullExecutionPlan(t, "env_exited", "run_exited")},
		Projections: &fakeProjection{}, Infrastructure: &fakeInfrastructure{}, Processes: processes,
		Readiness: &fakeReadiness{}, RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Start(context.Background(), fullStartRequest(t, "op_exited", "env_exited", "run_exited"))
	if !errors.Is(err, ErrProcessNotRunning) {
		t.Fatalf("start error: %v", err)
	}
	operation := journal.operation("op_exited")
	if operation.State != domain.OperationFailed || operation.FailureDetail == nil ||
		operation.FailureDetail.Code != FailureCodeServiceExited ||
		operation.FailureDetail.LogReference != "run_exited/services/service_web" ||
		operation.FailureDetail.NextAction != "inspect_operation_diagnostics" {
		t.Fatalf("exited service failure: state=%s detail=%+v", operation.State, operation.FailureDetail)
	}
}

// Finite preparation failures reference the step's own run directory, which
// the plan builder already names, instead of dropping it on the floor.
func TestPreparationFailurePersistsStepLogReference(t *testing.T) {
	journal := newMemoryJournal()
	spec := preparationSpec(t, "prepare-0")
	spec.ServiceID = "service_web"
	spec.LogReference = "run_prepare/preparations/service_web/prepare-0"
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7340, nil),
		Planner:         &staticPlanner{plan: ExecutionPlan{Preparations: []PreparationSpec{spec}}},
		Preparations:    &fakePreparations{err: errors.New("exit status 1: TOKEN=abc")},
		RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = coordinator.Start(context.Background(), StartRequest{
		OperationID: "op_prepare", EnvironmentID: "env_prepare", RunID: "run_prepare",
		Intent: &PlanIntent{ProfileDigest: "test", ServiceIDs: []string{"service_web"}},
	})
	operation := journal.operation("op_prepare")
	if operation.State != domain.OperationFailed || operation.FailureDetail == nil ||
		operation.FailureDetail.Code != FailureCodePreparationFailed ||
		operation.FailureDetail.Phase != PhasePreparingServices ||
		operation.FailureDetail.ResourceID != "service_web" || operation.FailureDetail.Step != "prepare-0" ||
		operation.FailureDetail.LogReference != spec.LogReference ||
		strings.Contains(operation.FailureDetail.Diagnostic, "TOKEN") {
		t.Fatalf("preparation failure: state=%s detail=%+v", operation.State, operation.FailureDetail)
	}
}

// A runner that already describes its failure keeps its own detail.
func TestRunnerProvidedFailureIsNotOverwritten(t *testing.T) {
	provided := OperationFailure{Code: "CUSTOM", Message: "custom.", LogReference: "run_x/preparations/s/c"}
	err := readinessFailure(context.Background(), "run_x", ServiceLaunch{ID: "s"}, safeTestOperationError{failure: provided})
	var provider OperationFailureProvider
	if !errors.As(err, &provider) || provider.OperationFailure().Code != "CUSTOM" {
		t.Fatalf("provided failure was replaced: %v", err)
	}
}
