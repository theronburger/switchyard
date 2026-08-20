package main

import (
	"context"
	"path/filepath"
	"testing"

	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/state"
)

// TestBootFailsInterruptedPreparationWithoutAnyProfile proves that a
// preparation interrupted by the previous daemon is failed on boot even when no
// accepted repository profile (and therefore no workspace coordinator) exists,
// so the worktree is not wedged by a permanently incomplete record.
func TestBootFailsInterruptedPreparationWithoutAnyProfile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := state.Open(ctx, state.Config{Path: filepath.Join(root, "state.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	journal, err := state.NewWorkspaceJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	record := workspacecontrol.OperationRecord{
		OperationID: "operation_prepare", WorktreeID: "worktree_01",
		State: workspacecontrol.StatePending, Phase: workspacecontrol.PhasePending,
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", StepCount: 2,
	}
	if err := journal.Begin(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State, record.Phase, record.NextStep = workspacecontrol.StateRunning, workspacecontrol.PhasePreparing, 1
	if err := journal.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	paths := applicationPaths{root: root, directory: root, database: filepath.Join(root, "state.sqlite"), configuration: filepath.Join(root, "configuration.yaml")}
	runtime, err := buildConfiguredProfileRuntime(ctx, store, paths, "daemon_01", repositoryInventory{}, func() {})
	if err != nil {
		t.Fatalf("boot without a profile: %v", err)
	}
	if runtime.actions != nil {
		t.Fatal("no environment actions expected without a profile")
	}
	incomplete, err := journal.Incomplete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(incomplete) != 0 {
		t.Fatalf("interrupted preparation survived boot: %+v", incomplete)
	}
	// The worktree can be prepared again.
	if err := journal.Begin(ctx, workspacecontrol.OperationRecord{
		OperationID: "operation_prepare_again", WorktreeID: "worktree_01",
		State: workspacecontrol.StatePending, Phase: workspacecontrol.PhasePending,
		Fingerprint: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", StepCount: 2,
	}); err != nil {
		t.Fatalf("worktree is still wedged: %v", err)
	}
}
