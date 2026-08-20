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

func TestOpenRejectsNewerDatabaseSchema(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, databasePath)
	futureVersion := migrations[len(migrations)-1].version + 1
	if _, err := store.database.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)", futureVersion, time.Now().UTC().Format(timeFormat)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := Open(ctx, Config{Path: databasePath})
	if !errors.Is(err, ErrUnsupportedSchemaVersion) {
		t.Fatalf("schema error: got %v, want %v", err, ErrUnsupportedSchemaVersion)
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

func TestSnapshotStorageRemainsBounded(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	for revision := 0; revision < 2_000; revision++ {
		snapshot := validSnapshot()
		snapshot.Daemon.State = fmt.Sprintf("observation-%d", revision)
		if _, err := store.CommitSnapshot(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}

	var rowCount int
	if err := store.database.QueryRowContext(ctx, "SELECT COUNT(*) FROM current_snapshot").Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("current snapshot rows: got %d, want 1", rowCount)
	}
	for _, legacyTable := range []string{"snapshot_head", "snapshot_revisions"} {
		var count int
		if err := store.database.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", legacyTable).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("legacy snapshot table %q still exists", legacyTable)
		}
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

func TestUpdateSnapshotMutatesLatestRevisionExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	committed, err := store.CommitSnapshot(ctx, validSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := store.UpdateSnapshot(ctx, func(snapshot *contractv1.StatusSnapshot) (bool, error) {
		if snapshot.SnapshotRevision != committed.SnapshotRevision {
			t.Fatalf("updater saw revision %d, want %d", snapshot.SnapshotRevision, committed.SnapshotRevision)
		}
		snapshot.Daemon.State = "observed"
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed || updated.SnapshotRevision != committed.SnapshotRevision+1 || updated.Daemon.State != "observed" {
		t.Fatalf("unexpected update: changed=%v snapshot=%#v", changed, updated)
	}
}

func TestUpdateSnapshotNoOpDoesNotAdvanceRevision(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	committed, err := store.CommitSnapshot(ctx, validSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := store.UpdateSnapshot(ctx, func(*contractv1.StatusSnapshot) (bool, error) {
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed || updated.SnapshotRevision != committed.SnapshotRevision {
		t.Fatalf("no-op update changed state: changed=%v revision=%d", changed, updated.SnapshotRevision)
	}
	stored, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SnapshotRevision != committed.SnapshotRevision {
		t.Fatalf("stored revision advanced: got %d, want %d", stored.SnapshotRevision, committed.SnapshotRevision)
	}
}

func TestOperationIdempotencyAndPersistence(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, databasePath)
	if _, err := store.CommitSnapshot(ctx, validSnapshot()); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := FingerprintRequest(map[string]string{"environmentId": "env_01"})
	if err != nil {
		t.Fatal(err)
	}
	request := NewOperation{
		ID:                 "operation_01",
		RunID:              "run_01",
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
	if got, want := createdOperation.State, "pending"; got != want {
		t.Fatalf("initial operation state: got %q, want %q", got, want)
	}
	if createdOperation.RunID != "run_01" {
		t.Fatalf("created operation run id: got %q", createdOperation.RunID)
	}
	assertSnapshotOperation(t, store, 2, createdOperation.ID, "pending")

	retriedOperation, created, err := store.CreateOperation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("idempotent retry created another operation")
	}
	if retriedOperation.ID != createdOperation.ID || retriedOperation.RunID != createdOperation.RunID {
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
	assertSnapshotOperation(t, store, 3, createdOperation.ID, "running")
	succeeded, err := store.TransitionOperation(ctx, createdOperation.ID, "succeeded", nil)
	if err != nil {
		t.Fatal(err)
	}
	if succeeded.State != "succeeded" {
		t.Fatalf("succeeded state: got %q", succeeded.State)
	}
	assertSnapshotOperation(t, store, 4, createdOperation.ID, "succeeded")
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
	assertSnapshotOperation(t, reopened, 4, createdOperation.ID, "succeeded")
}

func TestOperationExpectedEnvironmentRevisionIsAtomicAndRetrySafe(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	snapshot := validSnapshot()
	snapshot.Repositories = []contractv1.Repository{{
		ID: "repository_01", DisplayName: "repository", RootPath: "/tmp/repository",
		Adapter: "marketplace", Worktrees: []contractv1.Worktree{{
			ID: "worktree_01", Path: "/tmp/repository", HeadRevision: "abc",
		}},
	}}
	snapshot.Environments = []contractv1.Environment{operationTestEnvironment(7)}
	if _, err := store.CommitSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}

	requestBody := map[string]any{"environmentId": "environment_01", "expectedRevision": 7}
	fingerprint, err := FingerprintRequest(requestBody)
	if err != nil {
		t.Fatal(err)
	}
	expected := int64(7)
	request := NewOperation{
		ID: "operation_revision", RequestID: "request_revision", IdempotencyKey: "idempotency_revision",
		RequestFingerprint: fingerprint, Kind: "environment.stop", EnvironmentID: "environment_01",
		ExpectedEnvironmentRevision: &expected,
	}
	operation, created, err := store.CreateOperation(ctx, request)
	if err != nil || !created {
		t.Fatalf("create operation=%+v created=%t err=%v", operation, created, err)
	}

	changed := snapshot
	changed.Environments = []contractv1.Environment{operationTestEnvironment(8)}
	if _, err := store.CommitSnapshot(ctx, changed); err != nil {
		t.Fatal(err)
	}
	retried, created, err := store.CreateOperation(ctx, request)
	if err != nil || created || retried.ID != operation.ID {
		t.Fatalf("idempotent retry operation=%+v created=%t err=%v", retried, created, err)
	}
	if _, err := store.TransitionOperation(ctx, operation.ID, "running", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOperation(ctx, operation.ID, "succeeded", nil); err != nil {
		t.Fatal(err)
	}

	conflictingFingerprint, err := FingerprintRequest(map[string]any{"environmentId": "environment_01", "expectedRevision": 7, "other": true})
	if err != nil {
		t.Fatal(err)
	}
	_, created, err = store.CreateOperation(ctx, NewOperation{
		ID: "operation_conflict", RequestID: "request_conflict", IdempotencyKey: "idempotency_conflict",
		RequestFingerprint: conflictingFingerprint, Kind: "environment.stop", EnvironmentID: "environment_01",
		ExpectedEnvironmentRevision: &expected,
	})
	if !errors.Is(err, ErrEnvironmentRevisionConflict) || created {
		t.Fatalf("revision conflict created=%t err=%v", created, err)
	}
}

func TestOperationCreationSerializesEnvironmentMutations(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	if _, err := store.CommitSnapshot(ctx, validSnapshot()); err != nil {
		t.Fatal(err)
	}

	fingerprint, err := FingerprintRequest(map[string]string{"environmentId": "environment_01"})
	if err != nil {
		t.Fatal(err)
	}
	firstRequest := NewOperation{
		ID: "operation_first", RequestID: "request_first", IdempotencyKey: "idempotency_first",
		RequestFingerprint: fingerprint, Kind: "environment.start", EnvironmentID: "environment_01",
	}
	first, created, err := store.CreateOperation(ctx, firstRequest)
	if err != nil || !created {
		t.Fatalf("first operation=%+v created=%t err=%v", first, created, err)
	}
	retried, created, err := store.CreateOperation(ctx, firstRequest)
	if err != nil || created || retried.ID != first.ID {
		t.Fatalf("idempotent retry operation=%+v created=%t err=%v", retried, created, err)
	}

	secondRequest := NewOperation{
		ID: "operation_second", RequestID: "request_second", IdempotencyKey: "idempotency_second",
		RequestFingerprint: fingerprint, Kind: "environment.stop", EnvironmentID: "environment_01",
	}
	if _, created, err := store.CreateOperation(ctx, secondRequest); !errors.Is(err, ErrEnvironmentBusy) || created {
		t.Fatalf("concurrent operation created=%t err=%v", created, err)
	}
	if _, err := store.TransitionOperation(ctx, first.ID, "running", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOperation(ctx, first.ID, "succeeded", nil); err != nil {
		t.Fatal(err)
	}
	second, created, err := store.CreateOperation(ctx, secondRequest)
	if err != nil || !created {
		t.Fatalf("post-terminal operation=%+v created=%t err=%v", second, created, err)
	}
}

func operationTestEnvironment(revision int64) contractv1.Environment {
	return contractv1.Environment{
		ID: "environment_01", Revision: revision,
		RepositoryID: "repository_01", WorktreeID: "worktree_01", DisplayName: "environment",
		DesiredState: "running", ObservedState: "running", Health: "healthy",
		Services: []contractv1.Service{}, PortLeases: []contractv1.PortLease{},
		InfrastructureLeases: []contractv1.InfrastructureLease{}, URLs: map[string]string{},
		AttentionAlertIDs: []string{},
	}
}

func TestFailInterruptedOperationsIsAtomicAndPreservesTerminalWork(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	if _, err := store.CommitSnapshot(ctx, validSnapshot()); err != nil {
		t.Fatal(err)
	}

	createOperation := func(id string) contractv1.Operation {
		t.Helper()
		fingerprint, err := FingerprintRequest(map[string]string{"operationId": id})
		if err != nil {
			t.Fatal(err)
		}
		operation, _, err := store.CreateOperation(ctx, NewOperation{
			ID:                 id,
			RequestID:          "request_" + id,
			IdempotencyKey:     "idempotency_" + id,
			RequestFingerprint: fingerprint,
			Kind:               "startEnvironment",
		})
		if err != nil {
			t.Fatal(err)
		}
		return operation
	}

	interruptedOperation := createOperation("operation_interrupted")
	if _, err := store.TransitionOperation(ctx, interruptedOperation.ID, "running", nil); err != nil {
		t.Fatal(err)
	}
	completedOperation := createOperation("operation_completed")
	if _, err := store.TransitionOperation(ctx, completedOperation.ID, "running", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOperation(ctx, completedOperation.ID, "succeeded", nil); err != nil {
		t.Fatal(err)
	}

	interrupted, err := store.FailInterruptedOperations(ctx, contractv1.ContractError{
		Code:      "DAEMON_RESTARTED",
		Message:   "The daemon restarted before the operation completed.",
		Retryable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != interruptedOperation.ID ||
		interrupted[0].State != "failed" || interrupted[0].Error == nil ||
		interrupted[0].Error.Code != "DAEMON_RESTARTED" {
		t.Fatalf("interrupted operations: got %+v", interrupted)
	}

	snapshot, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := snapshot.SnapshotRevision, int64(7); got != want {
		t.Fatalf("reconciled snapshot revision: got %d, want %d", got, want)
	}
	states := make(map[string]string, len(snapshot.Operations))
	for _, operation := range snapshot.Operations {
		states[operation.ID] = operation.State
	}
	if states[interruptedOperation.ID] != "failed" || states[completedOperation.ID] != "succeeded" {
		t.Fatalf("reconciled snapshot states: got %+v", states)
	}

	repeated, err := store.FailInterruptedOperations(ctx, contractv1.ContractError{
		Code: "DAEMON_RESTARTED", Message: "Restarted.", Retryable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repeated) != 0 {
		t.Fatalf("terminal operations were reconciled twice: %+v", repeated)
	}
	afterRepeat, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterRepeat.SnapshotRevision != snapshot.SnapshotRevision {
		t.Fatalf("no-op reconciliation advanced snapshot to %d", afterRepeat.SnapshotRevision)
	}
}

func assertSnapshotOperation(t *testing.T, store *Store, revision int64, operationID, operationState string) {
	t.Helper()
	snapshot, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SnapshotRevision != revision {
		t.Fatalf("snapshot revision: got %d, want %d", snapshot.SnapshotRevision, revision)
	}
	if len(snapshot.Operations) != 1 || snapshot.Operations[0].ID != operationID ||
		snapshot.Operations[0].State != operationState {
		t.Fatalf("snapshot operations: got %+v", snapshot.Operations)
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

func TestEventHistoryRemainsBounded(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	for index := 1; index <= retainedEventLimit+25; index++ {
		if _, err := store.AppendEvent(ctx, events.NewEvent{
			ID: fmt.Sprintf("bounded_event_%d", index), Kind: "observation", Revision: int64(index),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var count int
	if err := store.database.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != retainedEventLimit {
		t.Fatalf("retained events: got %d, want %d", count, retainedEventLimit)
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
