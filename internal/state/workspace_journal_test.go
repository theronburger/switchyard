package state

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
)

func TestWorkspaceJournalPersistsIncompleteAndCurrentAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, path)
	snapshot := validSnapshot()
	snapshot.Repositories = []contractv2.Repository{{
		ID: "repository_01", DisplayName: "example", RootPath: "/tmp/repository", ProfileKey: "example",
		Worktrees: []contractv2.Worktree{{
			ID: "worktree_01", Path: "/tmp/worktree", HeadRevision: "abc",
			Git: contractv2.WorktreeState{},
		}},
	}}
	if _, err := store.CommitSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	journal, err := NewWorkspaceJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	record := validWorkspaceRecord("operation_01", "worktree_01")
	if err := journal.Begin(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State = workspacecontrol.StateRunning
	record.Phase = workspacecontrol.PhasePreparing
	record.NextStep = 1
	if err := journal.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openTestStore(t, path)
	journal, err = NewWorkspaceJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	incomplete, err := journal.Incomplete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(incomplete, []workspacecontrol.OperationRecord{record}) {
		t.Fatalf("reopened incomplete records: %+v", incomplete)
	}

	record.State = workspacecontrol.StateReady
	record.Phase = workspacecontrol.PhaseComplete
	record.NextStep = record.StepCount
	result := validWorkspaceResult("worktree_01")
	if err := journal.Publish(ctx, record, result); err != nil {
		t.Fatal(err)
	}
	current, found, err := journal.Current(ctx, "worktree_01")
	if err != nil || !found || !reflect.DeepEqual(current, result) {
		t.Fatalf("current workspace: found=%v result=%+v err=%v", found, current, err)
	}
	published, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workspaceStatus := published.Repositories[0].Worktrees[0].Workspace
	if workspaceStatus == nil || workspaceStatus.State != "ready" ||
		workspaceStatus.Toolchains[0].ID != "go" {
		t.Fatalf("workspace status was not published: %+v", workspaceStatus)
	}
	incomplete, err = journal.Incomplete(ctx)
	if err != nil || len(incomplete) != 0 {
		t.Fatalf("terminal workspace remained incomplete: %+v err=%v", incomplete, err)
	}
}

func TestWorkspaceJournalAllowsOnlyOneIncompleteOwnerPerWorktree(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	journal, err := NewWorkspaceJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	first := validWorkspaceRecord("operation_01", "worktree_01")
	if err := journal.Begin(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := validWorkspaceRecord("operation_02", "worktree_01")
	if err := journal.Begin(context.Background(), second); !errors.Is(err, ErrWorkspaceRecordExists) {
		t.Fatalf("second incomplete owner: %v", err)
	}
	first.State = workspacecontrol.StateFailed
	first.Phase = workspacecontrol.PhaseComplete
	first.FailureCode = "WORKSPACE_INTERRUPTED"
	if err := journal.Update(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := journal.Begin(context.Background(), second); err != nil {
		t.Fatalf("terminal record blocked retry: %v", err)
	}
}

func TestWorkspaceJournalRejectsCorruptCurrentResult(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	journal, err := NewWorkspaceJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	record := validWorkspaceRecord("operation_01", "worktree_01")
	if err := journal.Begin(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	record.State = workspacecontrol.StateRunning
	record.Phase = workspacecontrol.PhaseVerifying
	record.NextStep = record.StepCount
	if err := journal.Update(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	record.State = workspacecontrol.StateReady
	record.Phase = workspacecontrol.PhaseComplete
	record.NextStep = record.StepCount
	if err := journal.Publish(context.Background(), record, validWorkspaceResult("worktree_01")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(context.Background(), `
UPDATE workspace_current_results SET result_json = '{"WorktreeID":"foreign","extra":true}'
WHERE worktree_id = 'worktree_01'`); err != nil {
		t.Fatal(err)
	}
	_, _, err = journal.Current(context.Background(), "worktree_01")
	if !errors.Is(err, ErrWorkspaceResultInvalid) {
		t.Fatalf("corrupt result error: %v", err)
	}
}

func validWorkspaceRecord(operationID, worktreeID string) workspacecontrol.OperationRecord {
	return workspacecontrol.OperationRecord{
		OperationID: operationID, WorktreeID: worktreeID,
		State: workspacecontrol.StatePending, Phase: workspacecontrol.PhasePending,
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		StepCount:   2,
	}
}

func validWorkspaceResult(worktreeID string) workspacecontrol.Result {
	return workspacecontrol.Result{
		WorktreeID: worktreeID, ProfileKey: "example", WorktreeRoot: "/tmp/worktree",
		Ownership: workspacecontrol.OwnershipAdopted, State: workspacecontrol.StateReady,
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Toolchains: []workspacecontrol.Toolchain{{
			ID: "go", RequestedVersion: "1.26", ResolvedVersion: "1.26.5", Executable: "/usr/bin/go",
		}},
		PreparedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
	}
}
