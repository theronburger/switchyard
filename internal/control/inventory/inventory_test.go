package inventory

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

type fixedRepositoryReader struct {
	observation RepositoryObservation
}

func (reader fixedRepositoryReader) ReadRepository(context.Context, string) RepositoryObservation {
	return reader.observation
}

func TestDiscoverRepositoryProjectsStableOpaqueContractSnapshot(t *testing.T) {
	observation := validRepositoryObservation()
	inventoryService, err := New(fixedRepositoryReader{observation: observation})
	if err != nil {
		t.Fatal(err)
	}

	first := inventoryService.DiscoverRepository(context.Background(), "/checkout/link")
	if first.Repository == nil || len(first.Errors) != 0 {
		t.Fatalf("discovery result: %#v", first)
	}
	if first.Repository.RootPath != "/Users/example/configured repository" ||
		first.Repository.DisplayName != "configured repository" ||
		first.Repository.Adapter != "example" ||
		first.Repository.Remote != "owner/repository" {
		t.Fatalf("repository projection: %#v", first.Repository)
	}
	if len(first.Repository.Worktrees) != 2 || !first.Repository.Worktrees[0].IsPrimary {
		t.Fatalf("worktree ordering: %#v", first.Repository.Worktrees)
	}
	if !first.Repository.Worktrees[1].Git.Locked || !first.Repository.Worktrees[1].Git.Prunable {
		t.Fatalf("Git state projection: %#v", first.Repository.Worktrees[1].Git)
	}
	for _, identity := range []string{
		first.Repository.ID,
		first.Repository.Worktrees[0].ID,
		first.Repository.Worktrees[1].ID,
	} {
		if strings.Contains(identity, "configured repository") || strings.Contains(identity, "feature") ||
			strings.Contains(identity, "/Users/") {
			t.Fatalf("identity is not opaque: %q", identity)
		}
	}

	reordered := observation
	reordered.Worktrees = []WorktreeObservation{observation.Worktrees[1], observation.Worktrees[0]}
	reordered.Worktrees[1].Branch = "renamed-branch"
	reordered.Worktrees[1].HeadRevision = strings.Repeat("4", 40)
	reorderedInventory, err := New(fixedRepositoryReader{observation: reordered})
	if err != nil {
		t.Fatal(err)
	}
	second := reorderedInventory.DiscoverRepository(context.Background(), "/different/link")
	if second.Repository == nil {
		t.Fatalf("reordered discovery failed: %#v", second.Errors)
	}
	if first.Repository.ID != second.Repository.ID ||
		first.Repository.Worktrees[0].ID != second.Repository.Worktrees[0].ID ||
		first.Repository.Worktrees[1].ID != second.Repository.Worktrees[1].ID {
		t.Fatalf("stable identities changed:\nfirst: %#v\nsecond: %#v", first.Repository, second.Repository)
	}
}

func TestDiscoverRepositorySurfacesDeterministicStructuredProblems(t *testing.T) {
	observation := validRepositoryObservation()
	observation.Alerts = []AlertObservation{{
		Code:         AlertWorktreePrunable,
		WorktreePath: observation.Worktrees[1].Path,
	}}
	observation.Errors = []ErrorObservation{{
		Code:         ErrorWorktreeIdentityUnavailable,
		WorktreePath: observation.Worktrees[1].Path,
	}}
	inventoryService, err := New(fixedRepositoryReader{observation: observation})
	if err != nil {
		t.Fatal(err)
	}

	first := inventoryService.DiscoverRepository(context.Background(), "/checkout")
	second := inventoryService.DiscoverRepository(context.Background(), "/checkout")
	if !reflect.DeepEqual(first.Alerts, second.Alerts) || !reflect.DeepEqual(first.Errors, second.Errors) {
		t.Fatal("structured problem projection is not deterministic")
	}
	if len(first.Alerts) != 1 || first.Alerts[0].Code != AlertWorktreePrunable ||
		first.Alerts[0].WorktreeID == "" || first.Alerts[0].RepositoryID == "" {
		t.Fatalf("alert: %#v", first.Alerts)
	}
	if len(first.Errors) != 1 || first.Errors[0].Code != ErrorWorktreeIdentityUnavailable ||
		first.Errors[0].ResourceKind != "worktree" || first.Errors[0].ResourceID == "" {
		t.Fatalf("errors: %#v", first.Errors)
	}
}

func TestDiscoverRepositoryRefusesInvalidAdapterObservationWithoutEchoingData(t *testing.T) {
	observation := validRepositoryObservation()
	observation.Remote = ""
	observation.Errors = []ErrorObservation{{Code: ErrorRepositoryRemoteUnavailable}}
	inventoryService, err := New(fixedRepositoryReader{observation: observation})
	if err != nil {
		t.Fatal(err)
	}

	result := inventoryService.DiscoverRepository(context.Background(), "https://token@example.invalid/repo")
	if result.Repository != nil || len(result.Errors) != 1 {
		t.Fatalf("invalid result: %#v", result)
	}
	serialized := result.Errors[0].Error() + result.Errors[0].ResourceID
	if strings.Contains(serialized, "token") || strings.Contains(serialized, "example.invalid") {
		t.Fatalf("structured error exposed input data: %q", serialized)
	}
}

func TestDiscoverRepositoryRejectsPublicOrMismatchedExcludePath(t *testing.T) {
	observation := validRepositoryObservation()
	observation.SharedExcludePath = "/Users/example/configured repository/.gitignore"
	inventoryService, err := New(fixedRepositoryReader{observation: observation})
	if err != nil {
		t.Fatal(err)
	}

	result := inventoryService.DiscoverRepository(context.Background(), "/checkout")
	if result.Repository != nil || len(result.Errors) != 1 ||
		result.Errors[0].Code != ErrorAdapterObservationInvalid {
		t.Fatalf("unsafe exclude path was accepted: %#v", result)
	}
}

func validRepositoryObservation() RepositoryObservation {
	return RepositoryObservation{
		AdapterName:       "example",
		CommonDirectory:   "/Users/example/configured repository/.git",
		SharedExcludePath: "/Users/example/configured repository/.git/info/exclude",
		Remote:            "owner/repository",
		Worktrees: []WorktreeObservation{
			{
				Path:                   "/Users/example/configured repository",
				AdministrativeIdentity: "/Users/example/configured repository/.git",
				Branch:                 "main",
				HeadRevision:           strings.Repeat("1", 40),
				IsPrimary:              true,
			},
			{
				Path:                   "/Users/example/configured repository Worktrees/feature",
				AdministrativeIdentity: "/Users/example/configured repository/.git/worktrees/feature",
				Branch:                 "feature/ticket",
				HeadRevision:           strings.Repeat("2", 40),
				Locked:                 true,
				Prunable:               true,
			},
		},
	}
}
