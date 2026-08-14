package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

func TestEnvironmentJournalMigrationAndIncompleteRecordSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, path)
	seedEnvironmentJournalSnapshot(t, store)
	createPublicEnvironmentOperation(t, store, "operation_01", "env_01", environmentcontrol.OperationStart)
	journal := newTestEnvironmentJournal(t, store, defaultProjector)
	record := pendingStartRecord("operation_01", "env_01")
	if err := journal.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	record = runningRecord(record, environmentcontrol.PhaseLaunchingServices)
	if err := journal.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, path)
	journal = newTestEnvironmentJournal(t, reopened, defaultProjector)
	incomplete, err := journal.Incomplete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(incomplete) != 1 || incomplete[0].ID != record.ID || incomplete[0].Phase != environmentcontrol.PhaseLaunchingServices || incomplete[0].Rollback == nil {
		t.Fatalf("reopened incomplete record: %+v", incomplete)
	}
	for _, table := range []string{"environment_operation_records", "environment_current_results"} {
		var count int
		if err := reopened.database.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("migration did not create %s", table)
		}
	}
}

func TestEnvironmentJournalCreateRequiresMatchingPendingPublicOperation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prepare func(*testing.T, *Store)
		want    error
	}{
		{name: "missing", want: ErrEnvironmentOperationMismatch},
		{name: "kind", prepare: func(t *testing.T, store *Store) {
			createPublicEnvironmentOperation(t, store, "operation_01", "env_01", environmentcontrol.OperationStop)
		}, want: ErrEnvironmentOperationMismatch},
		{name: "environment", prepare: func(t *testing.T, store *Store) {
			createPublicEnvironmentOperation(t, store, "operation_01", "env_other", environmentcontrol.OperationStart)
		}, want: ErrEnvironmentOperationMismatch},
		{name: "not-pending", prepare: func(t *testing.T, store *Store) {
			createPublicEnvironmentOperation(t, store, "operation_01", "env_01", environmentcontrol.OperationStart)
			if _, err := store.TransitionOperation(context.Background(), "operation_01", "running", nil); err != nil {
				t.Fatal(err)
			}
		}, want: ErrEnvironmentOperationMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
			seedEnvironmentJournalSnapshot(t, store)
			if test.prepare != nil {
				test.prepare(t, store)
			}
			journal := newTestEnvironmentJournal(t, store, defaultProjector)
			err := journal.Create(context.Background(), pendingStartRecord("operation_01", "env_01"))
			if !errors.Is(err, test.want) {
				t.Fatalf("Create error: got %v, want %v", err, test.want)
			}
			var count int
			if err := store.database.QueryRow("SELECT COUNT(*) FROM environment_operation_records").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("mismatched Create persisted %d private records", count)
			}
		})
	}
}

func TestEnvironmentJournalCreateRejectsDuplicatePrivateRecord(t *testing.T) {
	t.Parallel()
	store, journal, record := preparedPendingJournal(t)
	if err := journal.Create(context.Background(), record); !errors.Is(err, ErrEnvironmentRecordExists) {
		t.Fatalf("duplicate error: %v", err)
	}
	operation, err := store.ReadOperation(context.Background(), record.ID)
	if err != nil || operation.State != "pending" {
		t.Fatalf("duplicate changed public operation: %+v err=%v", operation, err)
	}
}

func TestEnvironmentJournalEveryUpdateIsDurableAndPublishesOneRevision(t *testing.T) {
	t.Parallel()
	store, journal, record := preparedPendingJournal(t)
	phases := []environmentcontrol.OperationPhase{
		environmentcontrol.PhaseReservingPorts,
		environmentcontrol.PhasePreparingServices,
		environmentcontrol.PhaseMaterializing,
		environmentcontrol.PhaseEnsuringInfrastructure,
		environmentcontrol.PhaseLaunchingServices,
		environmentcontrol.PhaseWaitingReadiness,
		environmentcontrol.PhaseStoppingServices,
		environmentcontrol.PhaseStoppingInfrastructure,
		environmentcontrol.PhaseRemovingProjection,
		environmentcontrol.PhaseReleasingPorts,
		environmentcontrol.PhaseRollingBack,
	}
	previousRevision := snapshotRevision(t, store)
	for _, phase := range phases {
		record = runningRecord(record, phase)
		if err := journal.Update(context.Background(), record); err != nil {
			t.Fatalf("phase %s: %v", phase, err)
		}
		currentRevision := snapshotRevision(t, store)
		if currentRevision != previousRevision+1 {
			t.Fatalf("phase %s advanced revision %d -> %d", phase, previousRevision, currentRevision)
		}
		previousRevision = currentRevision
		incomplete, err := journal.Incomplete(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(incomplete) != 1 || incomplete[0].Phase != phase || incomplete[0].Rollback == nil {
			t.Fatalf("phase %s was not durable: %+v", phase, incomplete)
		}
		public, err := store.ReadOperation(context.Background(), record.ID)
		if err != nil || public.State != "running" {
			t.Fatalf("phase %s public operation: %+v err=%v", phase, public, err)
		}
	}
}

func TestEnvironmentJournalDurablyArmsProcessRollbackBeforeLaunch(t *testing.T) {
	t.Parallel()
	_, journal, record := preparedPendingJournal(t)
	record = runningRecord(record, environmentcontrol.PhaseLaunchingServices)
	record.Rollback = []environmentcontrol.RollbackEntry{{
		Kind: environmentcontrol.RollbackProcess, Armed: true,
		Process: &environmentcontrol.ServiceResult{
			ID: "app", EnvironmentID: record.EnvironmentID, RunID: record.RunID,
			OwnershipPath: "/private/run/ownership.json", Owned: true,
		},
	}}
	if err := journal.Update(context.Background(), record); err != nil {
		t.Fatalf("pre-launch rollback arm was not durable: %v", err)
	}
	incomplete, err := journal.Incomplete(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(incomplete) != 1 || len(incomplete[0].Rollback) != 1 || !incomplete[0].Rollback[0].Armed || incomplete[0].Rollback[0].Applied {
		t.Fatalf("armed process rollback record: %+v", incomplete)
	}
}

func TestEnvironmentJournalPublishIsAtomicSingleRevisionAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, path)
	seedEnvironmentJournalSnapshot(t, store)
	createPublicEnvironmentOperation(t, store, "operation_01", "env_01", environmentcontrol.OperationStart)
	journal := newTestEnvironmentJournal(t, store, defaultProjector)
	record := pendingStartRecord("operation_01", "env_01")
	if err := journal.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	record = runningRecord(record, environmentcontrol.PhaseWaitingReadiness)
	if err := journal.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	before := snapshotRevision(t, store)
	record.State = domain.OperationSucceeded
	record.EnvironmentState = domain.EnvironmentRunning
	record.Phase = environmentcontrol.PhaseComplete
	result := successfulEnvironmentResult("env_01")
	if err := journal.Publish(ctx, record, result); err != nil {
		t.Fatal(err)
	}
	if after := snapshotRevision(t, store); after != before+1 {
		t.Fatalf("Publish advanced revision %d -> %d", before, after)
	}

	snapshot, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Environments) != 1 || snapshot.Environments[0].ID != "env_01" || snapshot.Environments[0].Revision != 1 {
		t.Fatalf("published environment: %+v", snapshot.Environments)
	}
	if snapshot.Operations == nil || snapshot.Environments[0].Services == nil || snapshot.Environments[0].PortLeases == nil ||
		snapshot.Environments[0].InfrastructureLeases == nil || snapshot.Environments[0].URLs == nil || snapshot.Environments[0].AttentionAlertIDs == nil {
		t.Fatalf("published snapshot contains null collections: %+v", snapshot)
	}
	public, err := store.ReadOperation(ctx, record.ID)
	if err != nil || public.State != "succeeded" || public.EnvironmentRevision != 1 || public.Error != nil {
		t.Fatalf("public operation: %+v err=%v", public, err)
	}
	current, exists, err := journal.Current(ctx, "env_01")
	if err != nil || !exists || current.State != domain.EnvironmentRunning || current.Ports == nil || current.Infrastructure == nil || current.Services == nil {
		t.Fatalf("current result: %+v exists=%t err=%v", current, exists, err)
	}
	incomplete, err := journal.Incomplete(ctx)
	if err != nil || incomplete == nil || len(incomplete) != 0 {
		t.Fatalf("terminal record remained incomplete: %+v err=%v", incomplete, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	journal = newTestEnvironmentJournal(t, reopened, defaultProjector)
	current, exists, err = journal.Current(ctx, "env_01")
	if err != nil || !exists || current.EnvironmentID != "env_01" {
		t.Fatalf("reopened current result: %+v exists=%t err=%v", current, exists, err)
	}
}

func TestEnvironmentJournalProjectionFailureRollsBackEverything(t *testing.T) {
	t.Parallel()
	store, journal, record := preparedRunningJournal(t, func(*contractv1.Environment, environmentcontrol.EnvironmentResult) (contractv1.Environment, error) {
		return contractv1.Environment{}, errors.New("private projector detail")
	})
	before := snapshotRevision(t, store)
	terminal := record
	terminal.State = domain.OperationSucceeded
	terminal.EnvironmentState = domain.EnvironmentRunning
	terminal.Phase = environmentcontrol.PhaseComplete
	err := journal.Publish(context.Background(), terminal, successfulEnvironmentResult(record.EnvironmentID))
	if !errors.Is(err, ErrEnvironmentProjection) || strings.Contains(err.Error(), "private") {
		t.Fatalf("projection error: %v", err)
	}
	if after := snapshotRevision(t, store); after != before {
		t.Fatalf("failed projection advanced revision %d -> %d", before, after)
	}
	public, err := store.ReadOperation(context.Background(), record.ID)
	if err != nil || public.State != "running" {
		t.Fatalf("public operation changed: %+v err=%v", public, err)
	}
	incomplete, err := journal.Incomplete(context.Background())
	if err != nil || len(incomplete) != 1 || incomplete[0].State != domain.OperationRunning {
		t.Fatalf("private operation changed: %+v err=%v", incomplete, err)
	}
	if _, exists, err := journal.Current(context.Background(), record.EnvironmentID); err != nil || exists {
		t.Fatalf("failed projection published current result: exists=%t err=%v", exists, err)
	}
}

func TestEnvironmentJournalInvalidProjectionRollsBackStagedWrites(t *testing.T) {
	t.Parallel()
	store, journal, record := preparedRunningJournal(t, func(_ *contractv1.Environment, result environmentcontrol.EnvironmentResult) (contractv1.Environment, error) {
		return contractv1.Environment{ID: result.EnvironmentID, RepositoryID: "missing", WorktreeID: "missing"}, nil
	})
	before := snapshotRevision(t, store)
	terminal := record
	terminal.State = domain.OperationSucceeded
	terminal.EnvironmentState = domain.EnvironmentRunning
	terminal.Phase = environmentcontrol.PhaseComplete
	if err := journal.Publish(context.Background(), terminal, successfulEnvironmentResult(record.EnvironmentID)); !errors.Is(err, ErrEnvironmentProjection) {
		t.Fatalf("invalid projection error: %v", err)
	}
	if snapshotRevision(t, store) != before {
		t.Fatal("invalid projection advanced the snapshot")
	}
	public, err := store.ReadOperation(context.Background(), record.ID)
	if err != nil || public.State != "running" {
		t.Fatalf("invalid projection changed public operation: %+v err=%v", public, err)
	}
	if _, exists, err := journal.Current(context.Background(), record.EnvironmentID); err != nil || exists {
		t.Fatalf("invalid projection published a current result: exists=%t err=%v", exists, err)
	}
}

func TestEnvironmentJournalNeverPublishesRawFailure(t *testing.T) {
	t.Parallel()
	store, journal, record := preparedRunningJournal(t, defaultProjector)
	record.EnvironmentState = domain.EnvironmentStopping
	record.Phase = environmentcontrol.PhaseRollingBack
	if err := journal.Update(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	record.State = domain.OperationFailed
	record.EnvironmentState = domain.EnvironmentStopped
	record.Phase = environmentcontrol.PhaseComplete
	record.Failure = "secret filesystem path /Users/person/private and credential"
	result := successfulEnvironmentResult(record.EnvironmentID)
	result.State = domain.EnvironmentStopped
	if err := journal.Publish(context.Background(), record, result); err != nil {
		t.Fatal(err)
	}
	public, err := store.ReadOperation(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if public.Error == nil || public.Error.Code != "ENVIRONMENT_OPERATION_FAILED" || public.Error.Message != "Environment operation failed." {
		t.Fatalf("public error was not stable: %+v", public.Error)
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Users", "credential", "filesystem path"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public operation leaked %q: %s", forbidden, encoded)
		}
	}
	snapshot, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(snapshot)
	if strings.Contains(string(encoded), "credential") || strings.Contains(string(encoded), "Users") {
		t.Fatalf("status snapshot leaked private failure: %s", encoded)
	}
}

func TestEnvironmentJournalRejectsCorruptAndFutureRecordPayloads(t *testing.T) {
	t.Parallel()
	t.Run("operation-json", func(t *testing.T) {
		store, journal, record := preparedPendingJournal(t)
		if _, err := store.database.Exec("UPDATE environment_operation_records SET record_json = ? WHERE operation_id = ?", []byte(`{"broken":`), record.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := journal.Incomplete(context.Background()); !errors.Is(err, ErrEnvironmentRecordInvalid) {
			t.Fatalf("corrupt operation error: %v", err)
		}
	})
	t.Run("operation-version", func(t *testing.T) {
		store, journal, record := preparedPendingJournal(t)
		if _, err := store.database.Exec("UPDATE environment_operation_records SET schema_version = ? WHERE operation_id = ?", environmentRecordSchemaVersion+1, record.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := journal.Incomplete(context.Background()); !errors.Is(err, ErrEnvironmentRecordVersion) {
			t.Fatalf("future operation version error: %v", err)
		}
	})
	t.Run("result-json-and-version", func(t *testing.T) {
		store, journal, record := preparedRunningJournal(t, defaultProjector)
		record.State = domain.OperationSucceeded
		record.EnvironmentState = domain.EnvironmentRunning
		record.Phase = environmentcontrol.PhaseComplete
		if err := journal.Publish(context.Background(), record, successfulEnvironmentResult(record.EnvironmentID)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.database.Exec("UPDATE environment_current_results SET result_json = ?", []byte(`null`)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := journal.Current(context.Background(), record.EnvironmentID); !errors.Is(err, ErrEnvironmentResultInvalid) {
			t.Fatalf("corrupt result error: %v", err)
		}
		if _, err := store.database.Exec("UPDATE environment_current_results SET result_json = ?, schema_version = ?", []byte(`{}`), environmentRecordSchemaVersion+1); err != nil {
			t.Fatal(err)
		}
		if _, _, err := journal.Current(context.Background(), record.EnvironmentID); !errors.Is(err, ErrEnvironmentRecordVersion) {
			t.Fatalf("future result version error: %v", err)
		}
	})
}

func TestEnvironmentJournalConcurrentCreatesAndUpdatesAreSerializedAndOrdered(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	fixedNow := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	store, err := Open(ctx, Config{Path: path, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedEnvironmentJournalSnapshot(t, store)
	journal := newTestEnvironmentJournal(t, store, defaultProjector)
	const count = 16
	for index := range count {
		createPublicEnvironmentOperation(t, store, fmt.Sprintf("operation_%02d", index), fmt.Sprintf("env_%02d", index), environmentcontrol.OperationStart)
	}
	var group sync.WaitGroup
	errorsFound := make(chan error, count)
	for index := range count {
		group.Add(1)
		go func() {
			defer group.Done()
			record := pendingStartRecord(fmt.Sprintf("operation_%02d", index), fmt.Sprintf("env_%02d", index))
			if err := journal.Create(ctx, record); err != nil {
				errorsFound <- err
				return
			}
			if err := journal.Update(ctx, runningRecord(record, environmentcontrol.PhaseReservingPorts)); err != nil {
				errorsFound <- err
			}
		}()
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	incomplete, err := journal.Incomplete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(incomplete) != count {
		t.Fatalf("incomplete count: %d", len(incomplete))
	}
	ids := make([]string, len(incomplete))
	for index := range incomplete {
		ids[index] = incomplete[index].ID
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("incomplete operations are not deterministic: %v", ids)
	}
	first := append([]string(nil), ids...)
	again, err := journal.Incomplete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for index := range again {
		if again[index].ID != first[index] {
			t.Fatalf("ordering changed: first=%v again=%+v", first, again)
		}
	}
}

func TestEnvironmentJournalListCurrentPagesExactPersistedLeases(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	seedEnvironmentJournalSnapshot(t, store)
	journal := newTestEnvironmentJournal(t, store, defaultProjector)
	for index := 1; index <= 3; index++ {
		environmentID := fmt.Sprintf("env_%02d", index)
		operationID := fmt.Sprintf("operation_%02d", index)
		createPublicEnvironmentOperation(t, store, operationID, environmentID, environmentcontrol.OperationStart)
		record := pendingStartRecord(operationID, environmentID)
		if err := journal.Create(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		record = runningRecord(record, environmentcontrol.PhaseWaitingReadiness)
		if err := journal.Update(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		record.State = domain.OperationSucceeded
		record.EnvironmentState = domain.EnvironmentRunning
		record.Phase = environmentcontrol.PhaseComplete
		result := successfulEnvironmentResult(environmentID)
		result.Ports = []portlease.Lease{{
			Key:  portlease.Key{EnvironmentID: environmentID, ServiceID: "app", Purpose: "http"},
			Host: "127.0.0.1", Port: 4200 + index,
		}}
		if err := journal.Publish(context.Background(), record, result); err != nil {
			t.Fatal(err)
		}
	}

	first, err := journal.ListCurrent(context.Background(), "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Results) != 2 || !first.HasMore || first.NextEnvironmentID != "env_02" {
		t.Fatalf("first page: %+v", first)
	}
	for index, result := range first.Results {
		wantEnvironment := fmt.Sprintf("env_%02d", index+1)
		wantPort := 4201 + index
		if result.EnvironmentID != wantEnvironment || len(result.Ports) != 1 || result.Ports[0].Port != wantPort ||
			result.Ports[0].Host != "127.0.0.1" || result.Ports[0].Key.EnvironmentID != wantEnvironment {
			t.Fatalf("page lost exact persisted lease: %+v", result)
		}
	}
	second, err := journal.ListCurrent(context.Background(), first.NextEnvironmentID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Results) != 1 || second.HasMore || second.NextEnvironmentID != "env_03" || second.Results[0].Ports[0].Port != 4203 {
		t.Fatalf("second page: %+v", second)
	}
	empty, err := journal.ListCurrent(context.Background(), second.NextEnvironmentID, 2)
	if err != nil || empty.Results == nil || len(empty.Results) != 0 || empty.HasMore || empty.NextEnvironmentID != "env_03" {
		t.Fatalf("empty page: %+v err=%v", empty, err)
	}
}

func TestGenericInterruptedRecoveryLeavesEnvironmentJournalOperationsRunning(t *testing.T) {
	t.Parallel()
	store, journal, environmentRecord := preparedRunningJournal(t, defaultProjector)
	createPublicEnvironmentOperation(t, store, "operation_generic", "env_generic", environmentcontrol.OperationStart)
	before := snapshotRevision(t, store)
	interrupted, err := store.FailInterruptedOperations(context.Background(), contractv1.ContractError{
		Code: "DAEMON_RESTARTED", Message: "The daemon restarted.", Retryable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(interrupted) != 1 || interrupted[0].ID != "operation_generic" {
		t.Fatalf("generic recovery claimed environment operation: %+v", interrupted)
	}
	if after := snapshotRevision(t, store); after != before+1 {
		t.Fatalf("generic recovery revision: %d -> %d", before, after)
	}
	public, err := store.ReadOperation(context.Background(), environmentRecord.ID)
	if err != nil || public.State != "running" || public.Error != nil {
		t.Fatalf("environment public operation was generically failed: %+v err=%v", public, err)
	}
	incomplete, err := journal.Incomplete(context.Background())
	if err != nil || len(incomplete) != 1 || incomplete[0].ID != environmentRecord.ID {
		t.Fatalf("environment recovery lost ownership: %+v err=%v", incomplete, err)
	}
}

func preparedPendingJournal(t *testing.T) (*Store, *EnvironmentJournal, environmentcontrol.OperationRecord) {
	t.Helper()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	seedEnvironmentJournalSnapshot(t, store)
	record := pendingStartRecord("operation_01", "env_01")
	createPublicEnvironmentOperation(t, store, record.ID, record.EnvironmentID, record.Kind)
	journal := newTestEnvironmentJournal(t, store, defaultProjector)
	if err := journal.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return store, journal, record
}

func preparedRunningJournal(t *testing.T, projector EnvironmentProjector) (*Store, *EnvironmentJournal, environmentcontrol.OperationRecord) {
	t.Helper()
	store, journal, record := preparedPendingJournal(t)
	journal.projector = projector
	record = runningRecord(record, environmentcontrol.PhaseWaitingReadiness)
	if err := journal.Update(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return store, journal, record
}

func newTestEnvironmentJournal(t *testing.T, store *Store, projector EnvironmentProjector) *EnvironmentJournal {
	t.Helper()
	journal, err := NewEnvironmentJournal(store, projector)
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

func seedEnvironmentJournalSnapshot(t *testing.T, store *Store) {
	t.Helper()
	snapshot := validSnapshot()
	snapshot.Repositories = []contractv1.Repository{{
		ID: "repo_01", DisplayName: "Marketplace", Worktrees: []contractv1.Worktree{{ID: "worktree_01"}},
	}}
	if _, err := store.CommitSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
}

func createPublicEnvironmentOperation(
	t *testing.T,
	store *Store,
	operationID, environmentID string,
	kind environmentcontrol.OperationKind,
) {
	t.Helper()
	fingerprint, err := FingerprintRequest(map[string]string{"operationId": operationID})
	if err != nil {
		t.Fatal(err)
	}
	_, created, err := store.CreateOperation(context.Background(), NewOperation{
		ID: operationID, RequestID: "request_" + operationID, IdempotencyKey: "idempotency_" + operationID,
		RequestFingerprint: fingerprint, Kind: string(kind), EnvironmentID: environmentID,
	})
	if err != nil || !created {
		t.Fatalf("create public operation: created=%t err=%v", created, err)
	}
}

func pendingStartRecord(operationID, environmentID string) environmentcontrol.OperationRecord {
	return environmentcontrol.OperationRecord{
		ID: operationID, EnvironmentID: environmentID, RunID: "run_" + environmentID,
		Kind: environmentcontrol.OperationStart, State: domain.OperationPending,
		EnvironmentState: domain.EnvironmentUnknown, Phase: environmentcontrol.PhasePending,
		Rollback: []environmentcontrol.RollbackEntry{},
	}
}

func runningRecord(record environmentcontrol.OperationRecord, phase environmentcontrol.OperationPhase) environmentcontrol.OperationRecord {
	record.State = domain.OperationRunning
	if record.EnvironmentState == domain.EnvironmentUnknown {
		record.EnvironmentState = domain.EnvironmentStarting
	}
	record.Phase = phase
	return record
}

func successfulEnvironmentResult(environmentID string) environmentcontrol.EnvironmentResult {
	return environmentcontrol.EnvironmentResult{
		EnvironmentID: environmentID, RunID: "run_" + environmentID,
		State: domain.EnvironmentRunning, UpdatedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
		Ports: []portlease.Lease{}, Infrastructure: []containerhost.Goal{}, Services: []environmentcontrol.ServiceResult{},
	}
}

func defaultProjector(current *contractv1.Environment, result environmentcontrol.EnvironmentResult) (contractv1.Environment, error) {
	projected := contractv1.Environment{
		ID: result.EnvironmentID, RepositoryID: "repo_01", WorktreeID: "worktree_01", DisplayName: result.EnvironmentID,
		DesiredState: string(result.State), ObservedState: string(result.State), Health: "unknown",
	}
	if current != nil {
		projected.RepositoryID = current.RepositoryID
		projected.WorktreeID = current.WorktreeID
		projected.DisplayName = current.DisplayName
	}
	return projected, nil
}

func snapshotRevision(t *testing.T, store *Store) int64 {
	t.Helper()
	snapshot, err := store.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.SnapshotRevision
}
