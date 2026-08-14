package health

import (
	"errors"
	"testing"
	"time"
)

func TestCrashTrackerOpensStableAlertAtThresholdAndResolvesAfterRecovery(t *testing.T) {
	t.Parallel()
	tracker, err := NewCrashTracker("crash-loop:env-1:app", CrashLoopPolicy{
		Window: time.Minute, Threshold: 3, StableAfter: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	for index := range 2 {
		alert, recordErr := tracker.RecordExit(start.Add(time.Duration(index) * 10 * time.Second))
		if recordErr != nil {
			t.Fatal(recordErr)
		}
		if alert.Active || alert.Transition != AlertUnchanged {
			t.Fatalf("alert opened before threshold: %+v", alert)
		}
	}
	opened, err := tracker.RecordExit(start.Add(20 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !opened.Active || opened.Transition != AlertOpened || opened.Occurrences != 3 {
		t.Fatalf("alert did not open at threshold: %+v", opened)
	}
	firstSeen := opened.FirstSeenAt

	updated, err := tracker.RecordExit(start.Add(25 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if updated.AlertID != opened.AlertID || updated.FirstSeenAt != firstSeen || updated.Transition != AlertUpdated {
		t.Fatalf("alert identity was not stable: opened=%+v updated=%+v", opened, updated)
	}

	runningSince := start.Add(25 * time.Second)
	stillActive, err := tracker.Observe(start.Add(54*time.Second), runningSince)
	if err != nil {
		t.Fatal(err)
	}
	if !stillActive.Active || stillActive.Transition != AlertUnchanged {
		t.Fatalf("alert resolved before stable window: %+v", stillActive)
	}
	resolved, err := tracker.Observe(start.Add(55*time.Second), runningSince)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Active || resolved.Transition != AlertResolved || resolved.AlertID != opened.AlertID {
		t.Fatalf("alert did not resolve stably: %+v", resolved)
	}
	afterRecovery, err := tracker.RecordExit(start.Add(56 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if afterRecovery.Active || afterRecovery.Occurrences != 1 || !afterRecovery.FirstSeenAt.IsZero() {
		t.Fatalf("stable recovery did not reset crash accounting: %+v", afterRecovery)
	}
}

func TestCrashTrackerPrunesWindowAndRejectsOutOfOrderEvents(t *testing.T) {
	t.Parallel()
	tracker, err := NewCrashTracker("alert", CrashLoopPolicy{Window: 10 * time.Second, Threshold: 2, StableAfter: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	if _, err := tracker.RecordExit(start); err != nil {
		t.Fatal(err)
	}
	observation, err := tracker.RecordExit(start.Add(11 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if observation.Active || observation.Occurrences != 1 {
		t.Fatalf("old exit was not pruned: %+v", observation)
	}
	if _, err := tracker.RecordExit(start.Add(5 * time.Second)); !errors.Is(err, ErrOutOfOrderEvent) {
		t.Fatalf("expected out-of-order rejection, got %v", err)
	}
}
