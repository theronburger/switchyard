package state

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/domain"
)

// TestMigrationRewritesLegacyAdapterNamingFrom010State proves that a 0.1.0
// database whose snapshot, pinned environment intent, and workspace result
// still use the adapter naming is rewritten once into the v2 shape and then
// decodes through the strict record readers.
func TestMigrationRewritesLegacyAdapterNamingFrom010State(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, path)

	snapshot := validSnapshot()
	snapshot.Repositories = []contractv2.Repository{{
		ID: "repo_01", DisplayName: "example", RootPath: "/tmp/repository", ProfileKey: "example",
		Worktrees: []contractv2.Worktree{{ID: "worktree_01", Path: "/tmp/worktree", HeadRevision: "abc"}},
	}}
	if _, err := store.CommitSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	createPublicEnvironmentOperation(t, store, "operation_01", "env_01", environmentcontrol.OperationStart)
	environmentJournal := newTestEnvironmentJournal(t, store, defaultProjector)
	record := pendingStartRecord("operation_01", "env_01")
	record.Intent = &environmentcontrol.PlanIntent{ProfileDigest: "sha256:pinned", ServiceIDs: []string{"web"}}
	if err := environmentJournal.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	record = runningRecord(record, environmentcontrol.PhaseWaitingReadiness)
	if err := environmentJournal.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	createPublicEnvironmentOperation(t, store, "operation_02", "env_02", environmentcontrol.OperationStart)
	published := pendingStartRecord("operation_02", "env_02")
	published.Intent = &environmentcontrol.PlanIntent{ProfileDigest: "sha256:pinned", ServiceIDs: []string{"web"}}
	if err := environmentJournal.Create(ctx, published); err != nil {
		t.Fatal(err)
	}
	published = runningRecord(published, environmentcontrol.PhaseWaitingReadiness)
	if err := environmentJournal.Update(ctx, published); err != nil {
		t.Fatal(err)
	}
	published.State = domain.OperationSucceeded
	published.EnvironmentState = domain.EnvironmentRunning
	published.Phase = environmentcontrol.PhaseComplete
	if err := environmentJournal.Publish(ctx, published, successfulEnvironmentResult("env_02")); err != nil {
		t.Fatal(err)
	}
	workspaceJournal, err := NewWorkspaceJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	workspaceRecord := validWorkspaceRecord("operation_workspace", "worktree_01")
	if err := workspaceJournal.Begin(ctx, workspaceRecord); err != nil {
		t.Fatal(err)
	}
	workspaceRecord.State = workspacecontrol.StateRunning
	workspaceRecord.Phase = workspacecontrol.PhasePreparing
	workspaceRecord.NextStep = 1
	if err := workspaceJournal.Update(ctx, workspaceRecord); err != nil {
		t.Fatal(err)
	}
	workspaceRecord.State = workspacecontrol.StateReady
	workspaceRecord.Phase = workspacecontrol.PhaseComplete
	workspaceRecord.NextStep = 2
	if err := workspaceJournal.Publish(ctx, workspaceRecord, validWorkspaceResult("worktree_01")); err != nil {
		t.Fatal(err)
	}

	// Downgrade the persisted shapes to exactly what 0.1.0 wrote.
	downgrade := []struct{ statement, from, to string }{
		{"UPDATE current_snapshot SET payload_json = REPLACE(REPLACE(payload_json, ?, ?), '\"schemaVersion\":2', '\"schemaVersion\":1') WHERE singleton = 1", `"profileKey"`, `"adapter"`},
		{"UPDATE environment_operation_records SET schema_version = 1, record_json = REPLACE(record_json, ?, ?)", `"ProfileDigest"`, `"Adapter"`},
		{"UPDATE environment_current_results SET schema_version = 1", "", ""},
		{"UPDATE workspace_operation_records SET schema_version = 1", "", ""},
		{"UPDATE workspace_current_results SET schema_version = 1, result_json = REPLACE(result_json, ?, ?)", `"ProfileKey"`, `"Adapter"`},
		{"DELETE FROM schema_migrations WHERE version = 10", "", ""},
	}
	for _, step := range downgrade {
		var err error
		if step.from == "" {
			_, err = store.database.ExecContext(ctx, step.statement)
		} else {
			_, err = store.database.ExecContext(ctx, step.statement, step.from, step.to)
		}
		if err != nil {
			t.Fatalf("%s: %v", step.statement, err)
		}
	}
	var legacyPayload string
	if err := store.database.QueryRowContext(ctx, "SELECT payload_json FROM current_snapshot").Scan(&legacyPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(legacyPayload, `"adapter":"example"`) || !strings.Contains(legacyPayload, `"schemaVersion":1`) {
		t.Fatalf("legacy snapshot was not staged: %s", legacyPayload)
	}
	if _, err := store.ReadSnapshot(ctx); err == nil {
		t.Fatal("legacy snapshot unexpectedly decoded before migration")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, path)
	migrated, err := reopened.ReadSnapshot(ctx)
	if err != nil {
		t.Fatalf("migrated snapshot: %v", err)
	}
	if migrated.SchemaVersion != contractv2.SchemaVersion || migrated.SnapshotRevision <= snapshot.SnapshotRevision ||
		len(migrated.Repositories) != 1 || migrated.Repositories[0].ProfileKey != "example" || len(migrated.Environments) != 1 {
		t.Fatalf("migrated snapshot: %+v", migrated)
	}
	environmentJournal = newTestEnvironmentJournal(t, reopened, defaultProjector)
	incomplete, err := environmentJournal.Incomplete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(incomplete) != 1 || incomplete[0].ID != "operation_01" || incomplete[0].Intent == nil || incomplete[0].Intent.ProfileDigest != record.Intent.ProfileDigest {
		t.Fatalf("migrated incomplete record: %+v", incomplete)
	}
	current, found, err := environmentJournal.Current(ctx, "env_02")
	if err != nil || !found || current.RunID != "run_env_02" {
		t.Fatalf("migrated current result: %+v found=%v err=%v", current, found, err)
	}
	workspaceJournal, err = NewWorkspaceJournal(reopened)
	if err != nil {
		t.Fatal(err)
	}
	workspaceResult, found, err := workspaceJournal.Current(ctx, "worktree_01")
	if err != nil || !found || workspaceResult.ProfileKey != "example" {
		t.Fatalf("migrated workspace result: %+v found=%v err=%v", workspaceResult, found, err)
	}
	var legacyRows int
	if err := reopened.database.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM environment_operation_records WHERE schema_version = 1)
     + (SELECT COUNT(*) FROM environment_current_results WHERE schema_version = 1)
     + (SELECT COUNT(*) FROM workspace_operation_records WHERE schema_version = 1)
     + (SELECT COUNT(*) FROM workspace_current_results WHERE schema_version = 1)`).Scan(&legacyRows); err != nil {
		t.Fatal(err)
	}
	if legacyRows != 0 {
		t.Fatalf("%d legacy rows survived migration", legacyRows)
	}
}

// TestMigrationRefusesMalformedLegacyIntent proves that the daemon fails closed
// instead of guessing when a legacy pinned intent cannot be rewritten.
func TestMigrationRefusesMalformedLegacyIntent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, path)
	seedEnvironmentJournalSnapshot(t, store)
	createPublicEnvironmentOperation(t, store, "operation_01", "env_01", environmentcontrol.OperationStart)
	journal := newTestEnvironmentJournal(t, store, defaultProjector)
	record := pendingStartRecord("operation_01", "env_01")
	record.Intent = &environmentcontrol.PlanIntent{ProfileDigest: "sha256:pinned", ServiceIDs: []string{"web"}}
	if err := journal.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE environment_operation_records SET schema_version = 1, record_json = REPLACE(record_json, '"Intent":{', '"Intent":{"Adapter":"sha256:other",')`,
		"DELETE FROM schema_migrations WHERE version = 10",
	} {
		if _, err := store.database.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, Config{Path: path}); err == nil || !strings.Contains(err.Error(), "apply migration 10") {
		t.Fatalf("malformed legacy intent was accepted: %v", err)
	}
}
