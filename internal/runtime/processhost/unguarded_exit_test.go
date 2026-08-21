package processhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// When the kqueue exit watch cannot be established the child is reaped by a
// plain Wait. That Wait must never run under the run lock, otherwise every
// Observe, Reconcile, and Stop on the run would block for the child's whole
// life, and the run must still settle into a recorded exit afterwards.
func TestUnguardedChildNeverHoldsRunLockAcrossWait(t *testing.T) {
	original := watchExit
	watchExit = func(int) (<-chan struct{}, func(), error) {
		return nil, func() {}, errors.New("forced kqueue failure")
	}
	t.Cleanup(func() { watchExit = original })

	runDirectory := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	host := New(Config{GracePeriod: time.Second, KillWait: time.Second, PollInterval: 10 * time.Millisecond})
	ownership, err := host.Start(context.Background(), helperLaunchSpec(runDirectory, readyPath, "child"))
	if err != nil {
		t.Fatal(err)
	}
	ownershipPath := filepath.Join(runDirectory, OwnershipFileName)
	t.Cleanup(func() { stopOwnedRun(host, ownershipPath) })

	if host.unreapedChild(ownershipPath) {
		t.Fatal("an unguarded child was trusted as positively unreaped")
	}
	// The child is alive and unguarded; a lock holder must get through.
	observed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := host.Observe(ctx, ownershipPath)
		observed <- err
	}()
	select {
	case err := <-observed:
		if err != nil {
			t.Fatalf("observe unguarded run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run lock was held across Wait for an unguarded child")
	}
	if ownership.State != "running" {
		t.Fatalf("ownership state: %s", ownership.State)
	}

	// Stopping still reaches a recorded, stopped run.
	stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observation, err := host.Stop(stopContext, ownershipPath)
	if err != nil {
		t.Fatal(err)
	}
	if observation.State != "stopped" {
		t.Fatalf("stop observation: %+v", observation)
	}
	run := host.startedRun(ownershipPath)
	if run == nil {
		t.Fatal("started run was forgotten before its exit was recorded")
	}
	select {
	case <-run.recorded:
	case <-time.After(5 * time.Second):
		t.Fatal("exit of an unguarded child was never recorded")
	}
}
