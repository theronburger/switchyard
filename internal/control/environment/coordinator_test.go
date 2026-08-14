package environment

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

func TestStartAllocatesDistinctPortsAndLeavesForeignListenerAlone(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	foreignPort := listener.Addr().(*net.TCPAddr).Port
	if foreignPort > 65530 {
		t.Skip("ephemeral listener is too close to the end of the TCP port range")
	}
	allocator, err := portlease.NewAllocator(portlease.Config{
		FirstPort: foreignPort, LastPort: foreignPort + 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	journal := newMemoryJournal()
	coordinator, err := NewCoordinator(Config{Journal: journal, Ports: allocator})
	if err != nil {
		t.Fatal(err)
	}

	start := func(operationID, environmentID string) EnvironmentResult {
		t.Helper()
		result, err := coordinator.Start(context.Background(), StartRequest{
			OperationID: operationID, EnvironmentID: environmentID, RunID: "run_" + environmentID,
			Ports: []portlease.Reservation{{
				Key:            portlease.Key{EnvironmentID: environmentID, ServiceID: "service_web", Purpose: "http"},
				PreferredPorts: []int{foreignPort},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := start("op_first", "env_first")
	second := start("op_second", "env_second")
	if first.Ports[0].Port == second.Ports[0].Port {
		t.Fatalf("environments received the same port: %d", first.Ports[0].Port)
	}
	if first.Ports[0].Port == foreignPort || second.Ports[0].Port == foreignPort {
		t.Fatalf("foreign listener port %d was leased: %+v %+v", foreignPort, first.Ports, second.Ports)
	}

	connection, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("foreign listener did not survive allocation: %v", err)
	}
	_ = connection.Close()
}

func TestStartLateBindsExecutionPlanAfterAssignedPortsArePersisted(t *testing.T) {
	journal := newMemoryJournal()
	ports := newFakePorts(7055, nil)
	planner := &portBindingPlanner{runDirectory: t.TempDir(), journal: journal, operationID: "op_late"}
	processes := &fakeProcesses{journal: journal, operationID: "op_late"}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: ports, Planner: planner,
		Processes: processes, Readiness: &fakeReadiness{},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := portlease.Key{EnvironmentID: "env_late", ServiceID: "service_web", Purpose: "http"}

	result, err := coordinator.Start(context.Background(), StartRequest{
		OperationID: "op_late", EnvironmentID: "env_late", RunID: "run_late",
		Ports:  []portlease.Reservation{{Key: key, PreferredPorts: []int{7000}}},
		Intent: &PlanIntent{Adapter: "test", ServiceIDs: []string{"service_web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planner.seen) != 1 || len(planner.seen[0].AssignedPorts) != 1 ||
		planner.seen[0].AssignedPorts[0].Port != 7055 {
		t.Fatalf("planner did not receive assigned leases: %+v", planner.seen)
	}
	if len(processes.startSpecs) != 1 || !slices.Contains(processes.startSpecs[0].Environment, "PORT=7055") {
		t.Fatalf("process launch was not late-bound to the assigned port: %+v", processes.startSpecs)
	}
	if len(result.Services) != 1 || result.State != domain.EnvironmentRunning {
		t.Fatalf("late-bound result: %+v", result)
	}
}

func TestStartCheckpointsAndCompletesFinitePreparationsBeforeOtherSideEffects(t *testing.T) {
	journal := newMemoryJournal()
	calls := make([]string, 0)
	preparations := &fakePreparations{journal: journal, operationID: "op_prepare", calls: &calls}
	projection := &fakeProjection{journal: journal, operationID: "op_prepare", calls: &calls}
	planner := &staticPlanner{plan: ExecutionPlan{
		Preparations: []PreparationSpec{
			preparationSpec(t, "first"),
			preparationSpec(t, "second"),
		},
		Projection: &ProjectionRequest{ID: "projection"},
	}}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7080, &calls), Planner: planner,
		Preparations: preparations, Projections: projection,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Start(context.Background(), StartRequest{
		OperationID: "op_prepare", EnvironmentID: "env_prepare", RunID: "run_prepare",
		Intent: &PlanIntent{Adapter: "test", ServiceIDs: []string{"service_web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.EnvironmentRunning ||
		!slices.Equal(calls, []string{"prepare:first", "prepare:second", "apply-projection"}) {
		t.Fatalf("preparation order: result=%+v calls=%v", result, calls)
	}
	preparationUpdates := 0
	for _, event := range journal.events {
		if event == "update:op_prepare:"+string(PhasePreparingServices) {
			preparationUpdates++
		}
	}
	if preparationUpdates != 2 {
		t.Fatalf("each side effect was not checkpointed: %v", journal.events)
	}
}

func TestStartInitializesOwnedInfrastructureBeforeNativeServices(t *testing.T) {
	journal := newMemoryJournal()
	calls := make([]string, 0)
	plan := fullExecutionPlan(t, "env_initialize", "run_initialize")
	plan.Initializations = []PreparationSpec{preparationSpec(t, "initialize")}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7085, &calls), Planner: &staticPlanner{plan: plan},
		Projections: &fakeProjection{journal: journal, operationID: "op_initialize", calls: &calls},
		Infrastructure: &fakeInfrastructure{
			journal: journal, operationID: "op_initialize", calls: &calls,
		},
		Preparations: &fakePreparations{
			journal: journal, operationID: "op_initialize", calls: &calls,
		},
		Processes: &fakeProcesses{journal: journal, operationID: "op_initialize", calls: &calls},
		Readiness: &fakeReadiness{},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Start(
		context.Background(), fullStartRequest(t, "op_initialize", "env_initialize", "run_initialize"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.EnvironmentRunning {
		t.Fatalf("initialized result: %+v", result)
	}
	ensureIndex := slices.Index(calls, "ensure-infrastructure")
	initializeIndex := slices.Index(calls, "prepare:initialize")
	processIndex := slices.Index(calls, "start-process")
	if ensureIndex < 0 || initializeIndex <= ensureIndex || processIndex <= initializeIndex {
		t.Fatalf("infrastructure initialization order: %v", calls)
	}
	if !slices.Contains(
		journal.events,
		"update:op_initialize:"+string(PhaseInitializingInfrastructure),
	) {
		t.Fatalf("initialization checkpoint was not durable: %v", journal.events)
	}
}

func TestInitializationFailureRollsBackOwnedInfrastructureBeforeServices(t *testing.T) {
	journal := newMemoryJournal()
	calls := make([]string, 0)
	failure := errors.New("AWS_SECRET_ACCESS_KEY=secret@example.invalid")
	plan := fullExecutionPlan(t, "env_initialize_failure", "run_initialize_failure")
	plan.Initializations = []PreparationSpec{preparationSpec(t, "initialize-failure")}
	infrastructure := &fakeInfrastructure{
		journal: journal, operationID: "op_initialize_failure", calls: &calls,
	}
	processes := &fakeProcesses{
		journal: journal, operationID: "op_initialize_failure", calls: &calls,
	}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7086, &calls), Planner: &staticPlanner{plan: plan},
		Projections: &fakeProjection{
			journal: journal, operationID: "op_initialize_failure", calls: &calls,
		},
		Infrastructure: infrastructure,
		Preparations: &fakePreparations{
			journal: journal, operationID: "op_initialize_failure", calls: &calls, err: failure,
		},
		Processes: processes, Readiness: &fakeReadiness{}, RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Start(
		context.Background(),
		fullStartRequest(t, "op_initialize_failure", "env_initialize_failure", "run_initialize_failure"),
	)
	if !errors.Is(err, failure) {
		t.Fatalf("initialization failure: %v", err)
	}
	if result.State != domain.EnvironmentStopped || processes.starts != 0 ||
		infrastructure.ensureCalls != 1 || infrastructure.stopCalls != 1 {
		t.Fatalf(
			"unsafe initialization rollback: result=%+v starts=%d ensures=%d stops=%d calls=%v",
			result, processes.starts, infrastructure.ensureCalls, infrastructure.stopCalls, calls,
		)
	}
	wantSuffix := []string{"stop-infrastructure", "rollback-projection", "release-port"}
	if len(calls) < len(wantSuffix) || !slices.Equal(calls[len(calls)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("initialization rollback order: got %v, want suffix %v", calls, wantSuffix)
	}
	operation := journal.operation("op_initialize_failure")
	if operation.State != domain.OperationFailed || operation.Phase != PhaseComplete ||
		operation.Failure != "environment operation failed" || strings.Contains(operation.Failure, "secret") {
		t.Fatalf("unsafe persisted initialization failure: %+v", operation)
	}
}

func TestPreparationFailureAndCancellationAreDurablyRedacted(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		journal := newMemoryJournal()
		ports := newFakePorts(7090, nil)
		failure := errors.New("AWS_SECRET_ACCESS_KEY=secret@example.invalid")
		coordinator, err := NewCoordinator(Config{
			Journal: journal, Ports: ports,
			Planner: &staticPlanner{plan: ExecutionPlan{Preparations: []PreparationSpec{
				preparationSpec(t, "failure"),
			}}},
			Preparations:    &fakePreparations{journal: journal, operationID: "op_prepare_failure", err: failure},
			RollbackTimeout: time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = coordinator.Start(context.Background(), StartRequest{
			OperationID: "op_prepare_failure", EnvironmentID: "env_prepare_failure",
			RunID:  "run_prepare_failure",
			Intent: &PlanIntent{Adapter: "test", ServiceIDs: []string{"service_web"}},
		})
		if !errors.Is(err, failure) {
			t.Fatalf("preparation failure: %v", err)
		}
		operation := journal.operation("op_prepare_failure")
		if operation.State != domain.OperationFailed || operation.Failure != "environment operation failed" ||
			strings.Contains(operation.Failure, "secret") || strings.Contains(operation.Failure, "@") {
			t.Fatalf("persisted failure leaked details: %+v", operation)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		journal := newMemoryJournal()
		ports := newFakePorts(7095, nil)
		entered := make(chan struct{}, 1)
		block := make(chan struct{})
		coordinator, err := NewCoordinator(Config{
			Journal: journal, Ports: ports,
			Planner: &staticPlanner{plan: ExecutionPlan{Preparations: []PreparationSpec{
				preparationSpec(t, "cancel"),
			}}},
			Preparations: &fakePreparations{
				journal: journal, operationID: "op_prepare_cancel", entered: entered, block: block,
			},
			RollbackTimeout: time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := coordinator.Start(ctx, StartRequest{
				OperationID: "op_prepare_cancel", EnvironmentID: "env_prepare_cancel",
				RunID:  "run_prepare_cancel",
				Intent: &PlanIntent{Adapter: "test", ServiceIDs: []string{"service_web"}},
			})
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("preparation was not reached")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled preparation: %v", err)
		}
		operation := journal.operation("op_prepare_cancel")
		if operation.State != domain.OperationCancelled ||
			operation.Failure != "environment operation was cancelled" {
			t.Fatalf("cancelled operation: %+v", operation)
		}
	})
}

func preparationSpec(t *testing.T, id string) PreparationSpec {
	t.Helper()
	return PreparationSpec{
		ID: id, Executable: "/bin/echo", Arguments: []string{"prepare"},
		Environment: []string{"HOME=/tmp", "PATH=/usr/bin:/bin", "TMPDIR=/tmp"},
		Directory:   "/tmp", RunDirectory: filepath.Join(t.TempDir(), "preparations", id),
		Timeout: time.Minute,
	}
}

func TestPreparationRunDirectoriesCannotOverlapPersistentOwnership(t *testing.T) {
	t.Parallel()
	preparation := preparationSpec(t, "overlap")
	plan := ExecutionPlan{
		Preparations: []PreparationSpec{preparation},
		Services: []ServiceLaunch{{
			ID: "service_web",
			Process: processhost.LaunchSpec{
				EnvironmentID: "env_overlap", ServiceID: "service_web", RunID: "run_overlap",
				RunDirectory: filepath.Join(preparation.RunDirectory, "persistent"),
			},
		}},
	}
	if err := validateExecutionPlan("env_overlap", "run_overlap", nil, plan); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("overlapping preparation and process ownership was accepted: %v", err)
	}
}

func TestInitializationRequiresOwnedInfrastructure(t *testing.T) {
	t.Parallel()
	plan := ExecutionPlan{Initializations: []PreparationSpec{preparationSpec(t, "without-infrastructure")}}
	if err := validateExecutionPlan("env_initialize", "run_initialize", nil, plan); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("initialization without owned infrastructure was accepted: %v", err)
	}
}

func TestStartFailureRollsBackInReverseAfterPersistingOwnership(t *testing.T) {
	journal := newMemoryJournal()
	calls := make([]string, 0)
	ports := newFakePorts(7100, &calls)
	ports.guardErr = errors.New("ports reserved before rollback was persisted")
	ports.guard = func(RollbackKind) bool {
		return journal.persistedRollback("op_failure", RollbackPorts)
	}
	projection := &fakeProjection{journal: journal, operationID: "op_failure", calls: &calls}
	infrastructure := &fakeInfrastructure{journal: journal, operationID: "op_failure", calls: &calls}
	processes := &fakeProcesses{journal: journal, operationID: "op_failure", calls: &calls}
	readinessError := errors.New("service never became ready")
	planner := &staticPlanner{plan: fullExecutionPlan(t, "env_failure", "run_failure")}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: ports, Planner: planner, Projections: projection,
		Infrastructure: infrastructure, Processes: processes,
		Readiness: &fakeReadiness{err: readinessError}, RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := coordinator.Start(
		context.Background(), fullStartRequest(t, "op_failure", "env_failure", "run_failure"),
	)
	if !errors.Is(err, readinessError) {
		t.Fatalf("start error: got %v, want %v", err, readinessError)
	}
	if result.State != domain.EnvironmentStopped || ports.leaseCount() != 0 {
		t.Fatalf("rollback result: %+v, leases=%d", result, ports.leaseCount())
	}
	wantSuffix := []string{"stop-process", "stop-infrastructure", "rollback-projection", "release-port"}
	if len(calls) < len(wantSuffix) || !slices.Equal(calls[len(calls)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("rollback order: got %v, want suffix %v", calls, wantSuffix)
	}
	operation := journal.operation("op_failure")
	if operation.State != domain.OperationFailed || operation.Phase != PhaseComplete {
		t.Fatalf("failed operation: %+v", operation)
	}
}

func TestStartRefusesHealthyProbeWhenOwnedProcessAlreadyExited(t *testing.T) {
	journal := newMemoryJournal()
	processes := &fakeProcesses{reconcileObservation: processhost.Observation{
		State: "exited", OwnershipVerified: true,
	}}
	planner := &staticPlanner{plan: ExecutionPlan{Services: []ServiceLaunch{{
		ID: "service_web",
		Process: processhost.LaunchSpec{
			EnvironmentID: "env_false_healthy", ServiceID: "service_web", RunID: "run_false_healthy",
			Executable: "/bin/echo", Directory: "/tmp", RunDirectory: t.TempDir(),
		},
		Readiness: ReadinessSpec{ID: "health"},
	}}}}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7100, nil), Planner: planner,
		Processes: processes, Readiness: &fakeReadiness{}, RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Start(context.Background(), StartRequest{
		OperationID: "op_false_healthy", EnvironmentID: "env_false_healthy", RunID: "run_false_healthy",
		Intent: &PlanIntent{Adapter: "test", ServiceIDs: []string{"service_web"}},
	})
	if !errors.Is(err, ErrProcessNotRunning) {
		t.Fatalf("start error: got %v, want %v", err, ErrProcessNotRunning)
	}
	if result.State != domain.EnvironmentStopped || processes.stops != 1 {
		t.Fatalf("false healthy process was published: result=%+v stops=%d", result, processes.stops)
	}
	current, exists, currentErr := journal.Current(context.Background(), result.EnvironmentID)
	if currentErr != nil || !exists || current.State != domain.EnvironmentStopped || len(current.Services) != 0 {
		t.Fatalf("false healthy public result: current=%+v exists=%t err=%v", current, exists, currentErr)
	}
}

func TestStartCancellationRollsBackWithIndependentCleanupContext(t *testing.T) {
	journal := newMemoryJournal()
	ports := newFakePorts(7200, nil)
	projection := &fakeProjection{journal: journal, operationID: "op_cancel"}
	infrastructure := &fakeInfrastructure{journal: journal, operationID: "op_cancel"}
	processes := &fakeProcesses{journal: journal, operationID: "op_cancel"}
	planner := &staticPlanner{plan: fullExecutionPlan(t, "env_cancel", "run_cancel")}
	readinessEntered := make(chan struct{}, 1)
	readinessBlock := make(chan struct{})
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: ports, Planner: planner, Projections: projection,
		Infrastructure: infrastructure, Processes: processes,
		Readiness:       &fakeReadiness{entered: readinessEntered, block: readinessBlock},
		RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := fullStartRequest(t, "op_cancel", "env_cancel", "run_cancel")
	resultChannel := make(chan EnvironmentResult, 1)
	errorChannel := make(chan error, 1)
	go func() {
		result, err := coordinator.Start(ctx, request)
		resultChannel <- result
		errorChannel <- err
	}()
	select {
	case <-readinessEntered:
	case <-time.After(time.Second):
		t.Fatal("readiness was not reached")
	}
	cancel()
	result := <-resultChannel
	err = <-errorChannel
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("start error: got %v, want cancellation", err)
	}
	if result.State != domain.EnvironmentStopped || ports.leaseCount() != 0 ||
		processes.stops != 1 || infrastructure.stopCalls != 1 {
		t.Fatalf("cancel rollback: result=%+v leases=%d processStops=%d infraStops=%d",
			result, ports.leaseCount(), processes.stops, infrastructure.stopCalls)
	}
	if operation := journal.operation("op_cancel"); operation.State != domain.OperationCancelled {
		t.Fatalf("cancelled operation state: %s", operation.State)
	}
}

func TestFailedStartDoesNotReleaseAPreexistingStableLease(t *testing.T) {
	journal := newMemoryJournal()
	ports := newFakePorts(7250, nil)
	key := portlease.Key{EnvironmentID: "env_stable", ServiceID: "service_web", Purpose: "http"}
	if _, err := ports.ReserveSet(context.Background(), []portlease.Reservation{{Key: key}}); err != nil {
		t.Fatal(err)
	}
	applyError := errors.New("projection apply failed")
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: ports,
		Planner:     &staticPlanner{plan: ExecutionPlan{Projection: &ProjectionRequest{ID: "projection"}}},
		Projections: &fakeProjection{applyErr: applyError},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := coordinator.Start(context.Background(), StartRequest{
		OperationID: "op_stable", EnvironmentID: "env_stable", RunID: "run_stable",
		Ports:  []portlease.Reservation{{Key: key}},
		Intent: &PlanIntent{Adapter: "test", ServiceIDs: []string{"service_web"}},
	})
	if !errors.Is(err, applyError) {
		t.Fatalf("start error: got %v, want %v", err, applyError)
	}
	if ports.leaseCount() != 1 {
		t.Fatalf("failed start released a preexisting stable lease; count=%d", ports.leaseCount())
	}
	if len(result.Ports) != 1 || result.Ports[0].Key != key {
		t.Fatalf("stopped result lost its retained stable lease: %+v", result.Ports)
	}
}

func TestCoordinatorSerializesOneEnvironmentAndRunsDifferentEnvironmentsConcurrently(t *testing.T) {
	t.Run("same environment", func(t *testing.T) {
		journal := newMemoryJournal()
		ports := newFakePorts(7300, nil)
		release := make(chan struct{})
		entered := make(chan struct{}, 2)
		projection := &fakeProjection{blockApply: release, enteredApply: entered}
		planner := &staticPlanner{plan: ExecutionPlan{Projection: &ProjectionRequest{ID: "projection"}}}
		coordinator, err := NewCoordinator(Config{
			Journal: journal, Ports: ports, Planner: planner, Projections: projection,
		})
		if err != nil {
			t.Fatal(err)
		}
		request := func(operationID string) StartRequest {
			return StartRequest{
				OperationID: operationID, EnvironmentID: "env_same", RunID: "run_same",
				Intent: &PlanIntent{Adapter: "test", ServiceIDs: []string{"service_web"}},
			}
		}
		firstError := make(chan error, 1)
		secondError := make(chan error, 1)
		go func() { _, err := coordinator.Start(context.Background(), request("op_same_first")); firstError <- err }()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("first operation did not enter its side effect")
		}
		go func() {
			_, err := coordinator.Start(context.Background(), request("op_same_second"))
			secondError <- err
		}()
		select {
		case <-entered:
			t.Fatal("same-environment operation overlapped")
		case <-time.After(75 * time.Millisecond):
		}
		close(release)
		if err := <-firstError; err != nil {
			t.Fatal(err)
		}
		if err := <-secondError; !errors.Is(err, ErrInvalidState) {
			t.Fatalf("second start error: got %v, want invalid state", err)
		}
		if projection.maxConcurrent() != 1 {
			t.Fatalf("same environment max concurrency: %d", projection.maxConcurrent())
		}
	})

	t.Run("different environments", func(t *testing.T) {
		journal := newMemoryJournal()
		ports := newFakePorts(7400, nil)
		release := make(chan struct{})
		entered := make(chan struct{}, 2)
		projection := &fakeProjection{blockApply: release, enteredApply: entered}
		planner := &staticPlanner{plan: ExecutionPlan{Projection: &ProjectionRequest{ID: "projection"}}}
		coordinator, err := NewCoordinator(Config{
			Journal: journal, Ports: ports, Planner: planner, Projections: projection,
		})
		if err != nil {
			t.Fatal(err)
		}
		var wait sync.WaitGroup
		errorsSeen := make(chan error, 2)
		for _, environmentID := range []string{"env_parallel_a", "env_parallel_b"} {
			wait.Add(1)
			go func(environmentID string) {
				defer wait.Done()
				_, err := coordinator.Start(context.Background(), StartRequest{
					OperationID: "op_" + environmentID, EnvironmentID: environmentID,
					RunID:  "run_" + environmentID,
					Intent: &PlanIntent{Adapter: "test", ServiceIDs: []string{"service_web"}},
				})
				errorsSeen <- err
			}(environmentID)
		}
		for index := 0; index < 2; index++ {
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("different environments did not overlap")
			}
		}
		close(release)
		wait.Wait()
		close(errorsSeen)
		for err := range errorsSeen {
			if err != nil {
				t.Fatal(err)
			}
		}
		if projection.maxConcurrent() != 2 {
			t.Fatalf("different environment max concurrency: %d", projection.maxConcurrent())
		}
	})
}

func TestStopRefusesForeignResultBeforeAnyMutation(t *testing.T) {
	journal := newMemoryJournal()
	journal.putCurrent(EnvironmentResult{
		EnvironmentID: "env_foreign", RunID: "run_foreign", State: domain.EnvironmentRunning,
		Services: []ServiceResult{testServiceResult("env_foreign", "run_foreign", false)},
	})
	ports := newFakePorts(7500, nil)
	projection := &fakeProjection{}
	infrastructure := &fakeInfrastructure{}
	processes := &fakeProcesses{}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: ports, Projections: projection,
		Infrastructure: infrastructure, Processes: processes,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = coordinator.Stop(context.Background(), StopRequest{
		OperationID: "op_foreign", EnvironmentID: "env_foreign",
	})
	if !errors.Is(err, ErrForeignOwnership) {
		t.Fatalf("stop error: got %v, want %v", err, ErrForeignOwnership)
	}
	if processes.stops != 0 || infrastructure.stopCalls != 0 || len(journal.order) != 0 {
		t.Fatalf("foreign result caused calls: process=%d infra=%d operations=%v",
			processes.stops, infrastructure.stopCalls, journal.order)
	}
}

func TestStopReversesOnlyPersistedOwnedResources(t *testing.T) {
	journal := newMemoryJournal()
	calls := make([]string, 0)
	ports := newFakePorts(7550, &calls)
	key := portlease.Key{EnvironmentID: "env_stop", ServiceID: "service_web", Purpose: "http"}
	leases, err := ports.ReserveSet(context.Background(), []portlease.Reservation{{Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	plan := fullExecutionPlan(t, "env_stop", "run_stop")
	journal.putCurrent(EnvironmentResult{
		EnvironmentID: "env_stop", RunID: "run_stop", State: domain.EnvironmentRunning,
		Ports: leases,
		Projection: &ProjectionChange{
			ID: "projection", EnvironmentID: "env_stop", RunID: "run_stop",
			RollbackToken: "owned-token", Owned: true,
		},
		Infrastructure: plan.Infrastructure,
		Services:       []ServiceResult{testServiceResult("env_stop", "run_stop", true)},
	})
	projection := &fakeProjection{calls: &calls}
	infrastructure := &fakeInfrastructure{calls: &calls}
	processes := &fakeProcesses{calls: &calls}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: ports, Projections: projection,
		Infrastructure: infrastructure, Processes: processes,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := coordinator.Stop(context.Background(), StopRequest{
		OperationID: "op_stop", EnvironmentID: "env_stop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.EnvironmentStopped || len(result.Ports) != 0 ||
		len(result.Services) != 0 || len(result.Infrastructure) != 0 || result.Projection != nil {
		t.Fatalf("stopped result: %+v", result)
	}
	wantSuffix := []string{"stop-process", "stop-infrastructure", "rollback-projection", "release-port"}
	if len(calls) < len(wantSuffix) || !slices.Equal(calls[len(calls)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("stop order: got %v, want suffix %v", calls, wantSuffix)
	}
	if operation := journal.operation("op_stop"); operation.State != domain.OperationSucceeded {
		t.Fatalf("stop operation state: %s", operation.State)
	}
}

func TestRestartReconciliationRollsBackInterruptedStart(t *testing.T) {
	journal := newMemoryJournal()
	calls := make([]string, 0)
	ports := newFakePorts(7600, &calls)
	key := portlease.Key{EnvironmentID: "env_restart", ServiceID: "service_web", Purpose: "http"}
	leases, err := ports.ReserveSet(context.Background(), []portlease.Reservation{{Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	projection := ProjectionChange{
		ID: "projection", EnvironmentID: "env_restart", RunID: "run_restart",
		RollbackToken: "owned-token", Owned: true,
	}
	service := testServiceResult("env_restart", "run_restart", true)
	goal := fullExecutionPlan(t, "env_restart", "run_restart").Infrastructure[0]
	journal.putOperation(OperationRecord{
		ID: "op_restart", EnvironmentID: "env_restart", RunID: "run_restart",
		Kind: OperationStart, State: domain.OperationRunning,
		EnvironmentState: domain.EnvironmentStarting, Phase: PhaseLaunchingServices,
		Rollback: []RollbackEntry{
			{Kind: RollbackPorts, Armed: true, Applied: true, PortKeys: []portlease.Key{key}, Leases: leases},
			{Kind: RollbackProjection, Armed: true, Applied: true, Projection: &projection},
			{Kind: RollbackInfrastructure, Armed: true, Applied: true, Infrastructure: []containerhost.Goal{goal}},
			{Kind: RollbackProcess, Armed: true, Applied: true, Process: &service},
		},
	})
	infrastructure := &fakeInfrastructure{calls: &calls}
	processes := &fakeProcesses{calls: &calls}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: ports, Projections: &fakeProjection{calls: &calls},
		Infrastructure: infrastructure, Processes: processes, RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	outcomes, err := coordinator.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Err != nil || outcomes[0].State != domain.EnvironmentStopped {
		t.Fatalf("reconcile outcomes: %+v", outcomes)
	}
	wantSuffix := []string{"stop-process", "stop-infrastructure", "rollback-projection", "release-port"}
	if len(calls) < len(wantSuffix) || !slices.Equal(calls[len(calls)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("reconcile rollback order: got %v, want suffix %v", calls, wantSuffix)
	}
	if ports.leaseCount() != 0 {
		t.Fatalf("restart reconciliation leaked %d leases", ports.leaseCount())
	}
	operation := journal.operation("op_restart")
	if operation.State != domain.OperationFailed || operation.Phase != PhaseComplete {
		t.Fatalf("reconciled operation: %+v", operation)
	}
	result, exists, err := journal.Current(context.Background(), "env_restart")
	if err != nil || !exists || result.State != domain.EnvironmentStopped {
		t.Fatalf("reconciled environment: result=%+v exists=%t err=%v", result, exists, err)
	}
}

func TestRestartReconciliationRefusesAppliedLaunchWithMissingOwnership(t *testing.T) {
	journal := newMemoryJournal()
	service := testServiceResult("env_missing_ownership", "run_missing_ownership", true)
	journal.putOperation(OperationRecord{
		ID: "op_missing_ownership", EnvironmentID: service.EnvironmentID, RunID: service.RunID,
		Kind: OperationStart, State: domain.OperationRunning,
		EnvironmentState: domain.EnvironmentStarting, Phase: PhaseLaunchingServices,
		Rollback: []RollbackEntry{{
			Kind: RollbackProcess, Armed: true, Applied: true, Process: &service,
		}},
	})
	processes := &fakeProcesses{stopErr: os.ErrNotExist}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7600, nil), Processes: processes,
		RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	outcomes, err := coordinator.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || !errors.Is(outcomes[0].Err, processhost.ErrOrphanUnverified) ||
		outcomes[0].State != domain.EnvironmentFailed {
		t.Fatalf("reconcile outcomes: %+v", outcomes)
	}
	operation := journal.operation("op_missing_ownership")
	if operation.State != domain.OperationFailed || !operation.Rollback[0].Armed {
		t.Fatalf("missing ownership was marked clean: %+v", operation)
	}
	result, exists, err := journal.Current(context.Background(), service.EnvironmentID)
	if err != nil || !exists || result.State != domain.EnvironmentFailed || len(result.Services) != 1 {
		t.Fatalf("failed orphan result: result=%+v exists=%t err=%v", result, exists, err)
	}
}

func TestRestartReconciliationRefusesCrashWindowIntentEvidence(t *testing.T) {
	journal := newMemoryJournal()
	service := testServiceResult("env_crash_window", "run_crash_window", true)
	runDirectory := t.TempDir()
	service.OwnershipPath = filepath.Join(runDirectory, processhost.OwnershipFileName)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	intent := processhost.LaunchIntent{
		SchemaVersion: processhost.LaunchIntentSchemaVersion,
		EnvironmentID: service.EnvironmentID, ServiceID: service.ID, RunID: service.RunID,
		Executable: "/tmp/marketplace-service", LaunchFingerprint: strings.Repeat("0", 64),
		RunDirectory: runDirectory, CreatedAt: now, UpdatedAt: now,
	}
	intentPayload, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(runDirectory, processhost.LaunchIntentFileName), append(intentPayload, '\n'), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	journal.putOperation(OperationRecord{
		ID: "op_crash_window", EnvironmentID: service.EnvironmentID, RunID: service.RunID,
		Kind: OperationStart, State: domain.OperationRunning,
		EnvironmentState: domain.EnvironmentStarting, Phase: PhaseLaunchingServices,
		Rollback: []RollbackEntry{{
			Kind: RollbackProcess, Armed: true, Applied: false, Process: &service,
		}},
	})
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7600, nil), Processes: processhost.New(processhost.Config{}),
		RollbackTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	outcomes, err := coordinator.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || !errors.Is(outcomes[0].Err, processhost.ErrOrphanUnverified) ||
		outcomes[0].State != domain.EnvironmentFailed {
		t.Fatalf("reconcile outcomes: %+v", outcomes)
	}
	if operation := journal.operation("op_crash_window"); !operation.Rollback[0].Armed {
		t.Fatalf("crash-window orphan was marked clean: %+v", operation)
	}
}
