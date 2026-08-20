package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/state"
)

func TestManagedWorkspaceResolverAdoptsOnlyKnownNonPrimaryWorktrees(t *testing.T) {
	resolver := newManagedWorkspaceResolver(nil, repositoryInventory{Repositories: []contractv2.Repository{{
		ID: "repository_01", Worktrees: []contractv2.Worktree{
			{ID: "worktree_primary", Path: "/repo", IsPrimary: true},
			{ID: "worktree_linked", Path: "/repo-worktrees/linked"},
		},
	}}})
	resolved, err := resolver.ResolveAdopt(context.Background(), contractv2.AdoptWorktreeRequest{
		WorktreeID: "worktree_linked",
	})
	if err != nil || resolved.RepositoryID != "repository_01" || resolved.WorktreePath != "/repo-worktrees/linked" {
		t.Fatalf("resolved adoption: %+v err=%v", resolved, err)
	}
	for _, worktreeID := range []string{"worktree_primary", "worktree_missing"} {
		_, err := resolver.ResolveAdopt(context.Background(), contractv2.AdoptWorktreeRequest{WorktreeID: worktreeID})
		var actionError *daemon.ActionError
		if !errors.As(err, &actionError) {
			t.Fatalf("worktree=%s error=%v", worktreeID, err)
		}
		if worktreeID == "worktree_primary" && actionError.Contract.Code != "PRIMARY_WORKTREE_PROTECTED" {
			t.Fatalf("primary error: %+v", actionError.Contract)
		}
		if worktreeID == "worktree_missing" && actionError.Contract.Code != "WORKTREE_NOT_FOUND" {
			t.Fatalf("missing error: %+v", actionError.Contract)
		}
	}
}

func TestManagedWorkspaceResolverPreparesAnyKnownWorktree(t *testing.T) {
	resolver := newManagedWorkspaceResolver(nil, repositoryInventory{Repositories: []contractv2.Repository{{
		ID: "repository_01", Worktrees: []contractv2.Worktree{
			{ID: "worktree_primary", Path: "/repo", IsPrimary: true},
			{ID: "worktree_adopted", Path: "/external/worktree"},
		},
	}}})
	for _, worktreeID := range []string{"worktree_primary", "worktree_adopted"} {
		resolved, err := resolver.ResolvePrepare(context.Background(), contractv2.PrepareWorktreeRequest{WorktreeID: worktreeID})
		if err != nil || resolved != worktreeID {
			t.Fatalf("worktree=%s resolved=%s err=%v", worktreeID, resolved, err)
		}
	}
	_, err := resolver.ResolvePrepare(context.Background(), contractv2.PrepareWorktreeRequest{WorktreeID: "missing"})
	var actionError *daemon.ActionError
	if !errors.As(err, &actionError) || actionError.Contract.Code != "WORKTREE_NOT_FOUND" {
		t.Fatalf("missing error: %v", err)
	}
}

func TestManagedWorkspaceResolverRefusesToArchiveAnOccupiedWorktree(t *testing.T) {
	ctx := context.Background()
	store, err := state.Open(ctx, state.Config{Path: filepath.Join(t.TempDir(), "state.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	inventory := repositoryInventory{Complete: true, Repositories: []contractv2.Repository{{
		ID: "repository_01", DisplayName: "example", RootPath: "/repo", ProfileKey: "example",
		Worktrees: []contractv2.Worktree{
			{ID: "worktree_primary", Path: "/repo", IsPrimary: true, HeadRevision: "abc"},
			{ID: "worktree_linked", Path: "/repo-worktrees/linked", HeadRevision: "def"},
		},
	}}}
	snapshot := contractv2.StatusSnapshot{
		Daemon:       contractv2.DaemonStatus{InstanceID: "daemon_test", Version: "test", State: "ready", StartedAt: time.Now()},
		Environments: []contractv2.Environment{}, Operations: []contractv2.Operation{}, Alerts: []contractv2.Alert{},
	}
	if _, err := store.CommitSnapshot(ctx, mergeRepositoryInventory(snapshot, inventory)); err != nil {
		t.Fatal(err)
	}
	resolver := newManagedWorkspaceResolver(store, inventory)
	request := contractv2.ArchiveWorktreeRequest{WorktreeID: "worktree_linked"}
	if _, err := resolver.ResolveArchive(ctx, request); err != nil {
		t.Fatalf("unoccupied archive should resolve: %v", err)
	}

	lease, _, err := store.AcquireOccupancy(ctx, state.NewOccupancyLease{
		ID: "occupancy_01", RequestID: "request_01", WorktreeID: "worktree_linked",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.ResolveArchive(ctx, request)
	var actionError *daemon.ActionError
	if !errors.As(err, &actionError) || actionError.Contract.Code != "WORKTREE_OCCUPIED" || actionError.Status != 409 {
		t.Fatalf("occupied archive: %v", err)
	}

	// An inventory rebuild that forgets occupancy is repaired from the store,
	// and the published projection shows the held lease.
	rebuilt := inventory
	rebuilt.Repositories = []contractv2.Repository{{
		ID: "repository_01", DisplayName: "example", RootPath: "/repo", ProfileKey: "example",
		Worktrees: []contractv2.Worktree{
			{ID: "worktree_primary", Path: "/repo", IsPrimary: true, HeadRevision: "abc"},
			{ID: "worktree_linked", Path: "/repo-worktrees/linked", HeadRevision: "def"},
		},
	}}
	if err := restoreOccupancyInventory(ctx, store, &rebuilt); err != nil {
		t.Fatal(err)
	}
	if got := rebuilt.Repositories[0].Worktrees[1].Occupancy; len(got) != 1 || got[0].ID != lease.ID {
		t.Fatalf("restored occupancy: %+v", got)
	}
	if got := rebuilt.Repositories[0].Worktrees[0].Occupancy; len(got) != 0 {
		t.Fatalf("primary gained occupancy: %+v", got)
	}

	// Only an explicit release ends the protection.
	if _, err := store.ReleaseOccupancy(ctx, "worktree_linked", lease.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveArchive(ctx, request); err != nil {
		t.Fatalf("released worktree should archive: %v", err)
	}
}
