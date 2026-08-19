package main

import (
	"context"
	"errors"
	"testing"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/daemon"
)

func TestManagedWorkspaceResolverAdoptsOnlyKnownNonPrimaryWorktrees(t *testing.T) {
	resolver := newManagedWorkspaceResolver(nil, repositoryInventory{Repositories: []contractv1.Repository{{
		ID: "repository_01", Worktrees: []contractv1.Worktree{
			{ID: "worktree_primary", Path: "/repo", IsPrimary: true},
			{ID: "worktree_linked", Path: "/repo-worktrees/linked"},
		},
	}}})
	resolved, err := resolver.ResolveAdopt(context.Background(), contractv1.AdoptWorktreeRequest{
		WorktreeID: "worktree_linked",
	})
	if err != nil || resolved.RepositoryID != "repository_01" || resolved.WorktreePath != "/repo-worktrees/linked" {
		t.Fatalf("resolved adoption: %+v err=%v", resolved, err)
	}
	for _, worktreeID := range []string{"worktree_primary", "worktree_missing"} {
		_, err := resolver.ResolveAdopt(context.Background(), contractv1.AdoptWorktreeRequest{WorktreeID: worktreeID})
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
