package health

import (
	"context"
	"errors"
	"testing"
	"time"
)

type scriptedProber struct {
	results []ProbeResult
	errors  []error
	calls   int
}

func (prober *scriptedProber) Check(_ context.Context, spec ProbeSpec) (ProbeResult, error) {
	index := prober.calls
	prober.calls++
	if index < len(prober.errors) && prober.errors[index] != nil {
		return ProbeResult{}, prober.errors[index]
	}
	if index >= len(prober.results) {
		return ProbeResult{ProbeID: spec.ID, Kind: spec.Kind, Code: ResultOK, Success: true}, nil
	}
	result := prober.results[index]
	result.ProbeID = spec.ID
	result.Kind = spec.Kind
	return result, nil
}

func TestMonitorTransitionsFromStartingThroughUnhealthyAndRecovery(t *testing.T) {
	t.Parallel()
	prober := &scriptedProber{results: []ProbeResult{
		{Code: ResultUnavailable},
		{Code: ResultUnavailable},
		{Code: ResultUnavailable},
		{Code: ResultOK, Success: true},
	}}
	monitor := newTestMonitor(t, prober)
	spec := ServiceSpec{
		EnvironmentID: "env-1", ServiceID: "storefront", RunID: "run-1", FailureThreshold: 3,
		Readiness: []ProbeSpec{{ID: "ready", Kind: ProbeKindTCP}},
	}
	var runtime ServiceRuntimeState
	wantStates := []State{StateStarting, StateStarting, StateUnhealthy, StateHealthy}
	for index, expected := range wantStates {
		observation, err := monitor.Observe(context.Background(), spec, runtime, true)
		if err != nil {
			t.Fatal(err)
		}
		if observation.State != expected {
			t.Fatalf("observation %d: got %s, want %s", index, observation.State, expected)
		}
		runtime = observation.Runtime
	}
	if !runtime.EverReady || runtime.ConsecutiveFailures != 0 {
		t.Fatalf("recovery did not reset runtime state: %+v", runtime)
	}
}

func TestMonitorMarksPostReadinessFailureDegraded(t *testing.T) {
	t.Parallel()
	prober := &scriptedProber{results: []ProbeResult{{Code: ResultUnavailable}}}
	monitor := newTestMonitor(t, prober)
	observation, err := monitor.Observe(context.Background(), ServiceSpec{
		EnvironmentID: "env-1", ServiceID: "app", RunID: "run-1", FailureThreshold: 3,
		Readiness: []ProbeSpec{{ID: "ready", Kind: ProbeKindTCP}},
	}, ServiceRuntimeState{EverReady: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if observation.State != StateDegraded || observation.Runtime.ConsecutiveFailures != 1 {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestMonitorSeparatesReadinessAndHealth(t *testing.T) {
	t.Parallel()
	prober := &scriptedProber{results: []ProbeResult{
		{Code: ResultOK, Success: true},
		{Code: ResultUnexpectedStatus},
	}}
	monitor := newTestMonitor(t, prober)
	observation, err := monitor.Observe(context.Background(), ServiceSpec{
		EnvironmentID: "env-1", ServiceID: "app", RunID: "run-1",
		Readiness: []ProbeSpec{{ID: "ready", Kind: ProbeKindTCP}},
		Health:    []ProbeSpec{{ID: "health", Kind: ProbeKindHTTP}},
	}, ServiceRuntimeState{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.ReadinessReady || observation.HealthHealthy || observation.State != StateDegraded {
		t.Fatalf("readiness and health were not represented independently: %+v", observation)
	}
}

func TestMonitorDoesNotProbeStoppedProcess(t *testing.T) {
	t.Parallel()
	prober := &scriptedProber{}
	monitor := newTestMonitor(t, prober)
	observation, err := monitor.Observe(context.Background(), ServiceSpec{
		EnvironmentID: "env-1", ServiceID: "app", RunID: "run-1",
		Readiness: []ProbeSpec{{ID: "ready", Kind: ProbeKindTCP}},
	}, ServiceRuntimeState{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if observation.State != StateUnhealthy || prober.calls != 0 {
		t.Fatalf("stopped process observation: %+v, calls=%d", observation, prober.calls)
	}
}

func TestMonitorPropagatesCancellation(t *testing.T) {
	t.Parallel()
	prober := &scriptedProber{errors: []error{context.Canceled}}
	monitor := newTestMonitor(t, prober)
	_, err := monitor.Observe(context.Background(), ServiceSpec{
		EnvironmentID: "env-1", ServiceID: "app", RunID: "run-1",
		Readiness: []ProbeSpec{{ID: "ready", Kind: ProbeKindTCP}},
	}, ServiceRuntimeState{}, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func newTestMonitor(t *testing.T, prober ProbeRunner) *Monitor {
	t.Helper()
	backoff, err := NewBackoff(time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := NewMonitor(MonitorConfig{Prober: prober, Backoff: backoff})
	if err != nil {
		t.Fatal(err)
	}
	return monitor
}
