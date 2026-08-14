package environment

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

type observerProcessResult struct {
	observation processhost.Observation
	err         error
}

type observerProcesses struct {
	results    map[string]observerProcessResult
	reconciles []string
	starts     int
	stops      int
}

func (processes *observerProcesses) Start(
	context.Context,
	processhost.LaunchSpec,
) (processhost.Ownership, error) {
	processes.starts++
	return processhost.Ownership{}, errors.New("unexpected start")
}

func (processes *observerProcesses) Stop(
	context.Context,
	string,
) (processhost.Observation, error) {
	processes.stops++
	return processhost.Observation{}, errors.New("unexpected stop")
}

func (processes *observerProcesses) Reconcile(
	ctx context.Context,
	ownershipPath string,
) (processhost.Observation, error) {
	if err := ctx.Err(); err != nil {
		return processhost.Observation{}, err
	}
	processes.reconciles = append(processes.reconciles, ownershipPath)
	result, found := processes.results[ownershipPath]
	if !found {
		return processhost.Observation{}, errors.New("missing observation")
	}
	return result.observation, result.err
}

type observerReadiness struct {
	report HealthReport
	err    error
	seen   []ReadinessTarget
}

func (*observerReadiness) WaitReady(context.Context, ReadinessTarget) error {
	return errors.New("unexpected readiness wait")
}

func (readiness *observerReadiness) CheckHealth(
	ctx context.Context,
	target ReadinessTarget,
) (HealthReport, error) {
	if err := ctx.Err(); err != nil {
		return HealthReport{}, err
	}
	readiness.seen = append(readiness.seen, target)
	return readiness.report, readiness.err
}

func TestLiveObserverRefreshesRestoredProcessResourcesAndExactLeaseHealth(t *testing.T) {
	now := time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC)
	journal := newMemoryJournal()
	result := observedEnvironmentResult("env_restart_observe", "run_restart_observe", now.Add(-time.Minute))
	journal.putCurrent(result)
	ownershipBefore := result.Services[0].Process
	processes := &observerProcesses{results: map[string]observerProcessResult{
		result.Services[0].OwnershipPath: {observation: processhost.Observation{
			OwnershipPath: result.Services[0].OwnershipPath, State: "running", OwnershipVerified: true,
			MemberCount: 3, MemoryBytes: 8192, CPUTime: 42 * time.Second, ObservedAt: now,
		}},
	}}
	readiness := &observerReadiness{report: HealthReport{Readiness: "ready", Health: "unhealthy"}}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7700, nil), Processes: processes, Readiness: readiness,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewLiveObserver(LiveObserverConfig{
		Coordinator: coordinator, Interval: time.Second, Timeout: time.Second, Limit: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.RefreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	refreshed, exists, err := journal.Current(context.Background(), result.EnvironmentID)
	if err != nil || !exists {
		t.Fatalf("current environment: exists=%t err=%v", exists, err)
	}
	service := refreshed.Services[0]
	if service.Observation.State != "running" || service.Observation.ProcessCount != 3 ||
		service.Observation.MemoryBytes != 8192 || service.Observation.CPUPercent != 0 ||
		service.Health.Health != "unhealthy" {
		t.Fatalf("refreshed service: %+v", service)
	}
	if !reflect.DeepEqual(service.Process, ownershipBefore) {
		t.Fatal("live observation changed persisted process ownership")
	}
	if len(readiness.seen) != 1 || !reflect.DeepEqual(readiness.seen[0].Ports, result.Ports) ||
		readiness.seen[0].Spec != result.Services[0].Readiness {
		t.Fatalf("health did not use exact persisted lease/spec: %+v", readiness.seen)
	}
	if processes.starts != 0 || processes.stops != 0 || len(processes.reconciles) != 1 {
		t.Fatalf("observer mutated process host: %+v", processes)
	}
}

func TestLiveObserverPublishesProcessExitEvenIfPortProbeLooksHealthy(t *testing.T) {
	now := time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC)
	journal := newMemoryJournal()
	result := observedEnvironmentResult("env_process_exit", "run_process_exit", now.Add(-time.Minute))
	journal.putCurrent(result)
	processes := &observerProcesses{results: map[string]observerProcessResult{
		result.Services[0].OwnershipPath: {observation: processhost.Observation{
			State: "exited", OwnershipVerified: true, ObservedAt: now,
		}},
	}}
	readiness := &observerReadiness{report: HealthReport{Readiness: "ready", Health: "healthy"}}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7700, nil), Processes: processes, Readiness: readiness,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewLiveObserver(LiveObserverConfig{
		Coordinator: coordinator, Interval: time.Second, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.RefreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	refreshed, _, _ := journal.Current(context.Background(), result.EnvironmentID)
	service := refreshed.Services[0]
	if service.Observation.State != "exited" || service.Observation.ProcessCount != 0 ||
		service.Health.Readiness != "not_ready" || service.Health.Health != "unhealthy" {
		t.Fatalf("exited service observation: %+v", service)
	}
}

func TestLiveObserverDegradesUnverifiableOwnershipWithoutDroppingIt(t *testing.T) {
	now := time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC)
	journal := newMemoryJournal()
	result := observedEnvironmentResult("env_foreign", "run_foreign", now.Add(-time.Minute))
	journal.putCurrent(result)
	ownershipBefore := result.Services[0].Process
	processes := &observerProcesses{results: map[string]observerProcessResult{
		result.Services[0].OwnershipPath: {err: processhost.ErrOwnershipMismatch},
	}}
	readiness := &observerReadiness{report: HealthReport{Readiness: "ready", Health: "healthy"}}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7700, nil), Processes: processes, Readiness: readiness,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewLiveObserver(LiveObserverConfig{
		Coordinator: coordinator, Interval: time.Second, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.RefreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	refreshed, _, _ := journal.Current(context.Background(), result.EnvironmentID)
	service := refreshed.Services[0]
	if service.Observation.State != "unverifiable" ||
		service.Observation.Code != ServiceObservationOwnershipUnverified || service.Health.Health != "degraded" {
		t.Fatalf("unverifiable service: %+v", service)
	}
	if !reflect.DeepEqual(service.Process, ownershipBefore) || !service.Owned || len(refreshed.Services) != 1 {
		t.Fatal("unverifiable observation dropped ownership")
	}
	if processes.starts != 0 || processes.stops != 0 {
		t.Fatalf("unverifiable observation mutated processes: %+v", processes)
	}
}

func TestLiveObserverRunStopsOnDaemonCancellation(t *testing.T) {
	journal := newMemoryJournal()
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Ports: newFakePorts(7700, nil), Processes: &observerProcesses{},
		Readiness: &observerReadiness{},
	})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewLiveObserver(LiveObserverConfig{
		Coordinator: coordinator, Interval: time.Hour, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("observer error: got %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not stop after daemon cancellation")
	}
}

func observedEnvironmentResult(environmentID, runID string, updatedAt time.Time) EnvironmentResult {
	service := testServiceResult(environmentID, runID, true)
	service.Readiness = ReadinessSpec{ID: "persisted.health.v1"}
	service.Health = HealthReport{Readiness: "ready", Health: "healthy"}
	service.Process.Members = []processhost.ProcessIdentity{{PID: 1234}}
	service.Observation = ServiceObservation{
		State: "running", ProcessCount: 1, MemoryBytes: 1024, ObservedAt: updatedAt,
	}
	return EnvironmentResult{
		EnvironmentID: environmentID, RunID: runID, State: domain.EnvironmentRunning,
		Ports: []portlease.Lease{{
			Key:  portlease.Key{EnvironmentID: environmentID, ServiceID: service.ID, Purpose: "http"},
			Host: "127.0.0.1", Port: 31001,
		}},
		Infrastructure: []containerhost.Goal{}, Services: []ServiceResult{service}, UpdatedAt: updatedAt,
	}
}
