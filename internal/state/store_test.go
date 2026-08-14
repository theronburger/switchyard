package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/events"
)

func TestOpenEnablesWALAndExcludesSecondDaemon(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, databasePath)

	var journalMode string
	if err := store.database.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if got, want := journalMode, "wal"; got != want {
		t.Fatalf("journal mode: got %q, want %q", got, want)
	}

	_, err := Open(ctx, Config{Path: databasePath})
	if !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("second open error: got %v, want %v", err, ErrStoreLocked)
	}
}

func TestCommitSnapshotAdvancesRevisionAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "state.sqlite")
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	store, err := Open(ctx, Config{Path: databasePath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.CommitSnapshot(ctx, validSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := first.SnapshotRevision, int64(1); got != want {
		t.Fatalf("first revision: got %d, want %d", got, want)
	}

	now = now.Add(time.Second)
	second, err := store.CommitSnapshot(ctx, validSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := second.SnapshotRevision, int64(2); got != want {
		t.Fatalf("second revision: got %d, want %d", got, want)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	stored, err := reopened.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stored.SnapshotRevision, int64(2); got != want {
		t.Fatalf("stored revision: got %d, want %d", got, want)
	}
	if !stored.GeneratedAt.Equal(now) {
		t.Fatalf("stored generation time: got %s, want %s", stored.GeneratedAt, now)
	}
}

func TestConcurrentSnapshotCommitsRemainAtomic(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))

	const writerCount = 20
	var waitGroup sync.WaitGroup
	errorsFound := make(chan error, writerCount)
	for writer := 0; writer < writerCount; writer++ {
		waitGroup.Add(1)
		go func(writer int) {
			defer waitGroup.Done()
			snapshot := validSnapshot()
			snapshot.Daemon.State = fmt.Sprintf("writer-%d", writer)
			if _, err := store.CommitSnapshot(ctx, snapshot); err != nil {
				errorsFound <- err
			}
		}(writer)
	}
	waitGroup.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}

	stored, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stored.SnapshotRevision, int64(writerCount); got != want {
		t.Fatalf("stored revision: got %d, want %d", got, want)
	}
	if err := stored.Validate(); err != nil {
		t.Fatalf("stored snapshot is partial: %v", err)
	}
}

func TestOperationIdempotencyAndPersistence(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, databasePath)
	fingerprint, err := FingerprintRequest(map[string]string{"environmentId": "env_01"})
	if err != nil {
		t.Fatal(err)
	}
	request := NewOperation{
		ID:                 "operation_01",
		RequestID:          "request_01",
		IdempotencyKey:     "idempotency_01",
		RequestFingerprint: fingerprint,
		Kind:               "startEnvironment",
		EnvironmentID:      "env_01",
	}

	createdOperation, created, err := store.CreateOperation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first operation was not created")
	}

	retriedOperation, created, err := store.CreateOperation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("idempotent retry created another operation")
	}
	if retriedOperation.ID != createdOperation.ID {
		t.Fatalf("retried operation: got %q, want %q", retriedOperation.ID, createdOperation.ID)
	}

	differentFingerprint, err := FingerprintRequest(map[string]string{"environmentId": "env_02"})
	if err != nil {
		t.Fatal(err)
	}
	request.RequestFingerprint = differentFingerprint
	request.ID = "operation_02"
	_, _, err = store.CreateOperation(ctx, request)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error: got %v, want %v", err, ErrIdempotencyConflict)
	}

	running, err := store.TransitionOperation(ctx, createdOperation.ID, "running", nil)
	if err != nil {
		t.Fatal(err)
	}
	if running.State != "running" {
		t.Fatalf("running state: got %q", running.State)
	}
	succeeded, err := store.TransitionOperation(ctx, createdOperation.ID, "succeeded", nil)
	if err != nil {
		t.Fatal(err)
	}
	if succeeded.State != "succeeded" {
		t.Fatalf("succeeded state: got %q", succeeded.State)
	}
	_, err = store.TransitionOperation(ctx, createdOperation.ID, "running", nil)
	if !errors.Is(err, ErrInvalidOperationTransition) {
		t.Fatalf("terminal transition error: got %v, want %v", err, ErrInvalidOperationTransition)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, databasePath)
	persisted, err := reopened.ReadOperation(ctx, createdOperation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := persisted.State, "succeeded"; got != want {
		t.Fatalf("persisted state: got %q, want %q", got, want)
	}
}

func TestEventsResumeFromCursor(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	for index := 1; index <= 3; index++ {
		_, err := store.AppendEvent(ctx, events.NewEvent{
			ID:       fmt.Sprintf("event_%d", index),
			Revision: int64(index),
			Kind:     "snapshotCommitted",
			Payload:  json.RawMessage(fmt.Sprintf(`{"index":%d}`, index)),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	firstPage, err := store.ReadEvents(ctx, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(firstPage.Events), 2; got != want {
		t.Fatalf("first page size: got %d, want %d", got, want)
	}
	if !firstPage.HasMore {
		t.Fatal("first page did not report more events")
	}

	secondPage, err := store.ReadEvents(ctx, firstPage.NextCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(secondPage.Events), 1; got != want {
		t.Fatalf("second page size: got %d, want %d", got, want)
	}
	if got, want := secondPage.Events[0].ID, "event_3"; got != want {
		t.Fatalf("resumed event: got %q, want %q", got, want)
	}
}

func openTestStore(t *testing.T, databasePath string) *Store {
	t.Helper()
	store, err := Open(context.Background(), Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func validSnapshot() contractv1.StatusSnapshot {
	return contractv1.StatusSnapshot{
		Daemon: contractv1.DaemonStatus{
			InstanceID: "daemon_test",
			Version:    "test",
			State:      "ready",
			StartedAt:  time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC),
		},
		Repositories: []contractv1.Repository{},
		Environments: []contractv1.Environment{},
		Operations:   []contractv1.Operation{},
		Alerts:       []contractv1.Alert{},
	}
}
