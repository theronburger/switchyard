package health

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBackoffCapsWithoutOverflow(t *testing.T) {
	t.Parallel()
	backoff, err := NewBackoff(10*time.Millisecond, 75*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{0, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 75 * time.Millisecond, 75 * time.Millisecond}
	for attempt, expected := range want {
		if actual := backoff.Delay(uint(attempt)); actual != expected {
			t.Fatalf("attempt %d: got %s, want %s", attempt, actual, expected)
		}
	}
	if actual := backoff.Delay(^uint(0)); actual != 75*time.Millisecond {
		t.Fatalf("huge attempt did not cap: %s", actual)
	}
}

func TestWaitHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	startedAt := time.Now()
	if err := Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func TestCadenceAdaptsOnlyNonessentialChecks(t *testing.T) {
	t.Parallel()
	policy := CadencePolicy{Healthy: time.Second, Maximum: 5 * time.Second}
	pressured := AdaptiveInputs{LowPowerMode: true, ThermalPressure: ThermalCritical}
	if actual := policy.Interval(StateHealthy, pressured); actual != 5*time.Second {
		t.Fatalf("pressured interval = %s", actual)
	}
	pressured.Essential = true
	if actual := policy.Interval(StateHealthy, pressured); actual != time.Second {
		t.Fatalf("essential interval = %s", actual)
	}
}

func TestCadenceUsesBoundedDefaultWhenMaximumIsOmitted(t *testing.T) {
	t.Parallel()
	policy := CadencePolicy{Healthy: time.Second}
	actual := policy.Interval(StateHealthy, AdaptiveInputs{LowPowerMode: true})
	if actual != 2*time.Second {
		t.Fatalf("adaptive interval = %s", actual)
	}
}
