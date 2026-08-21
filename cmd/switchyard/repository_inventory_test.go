package main

import (
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

func TestMergeRepositoryInventoryPreservesRepositoriesWithEnvironments(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	previousRepository := inventoryTestRepository("repo_previous", "worktree_previous", "/tmp/previous")
	discoveredRepository := inventoryTestRepository("repo_discovered", "worktree_discovered", "/tmp/discovered")
	snapshot := contractv2.StatusSnapshot{
		SchemaVersion: contractv2.SchemaVersion,
		Daemon: contractv2.DaemonStatus{
			InstanceID: "daemon_test", Version: version, State: "ready", StartedAt: now,
		},
		Repositories: []contractv2.Repository{previousRepository},
		Environments: []contractv2.Environment{{
			ID:                   "env_previous",
			RepositoryID:         previousRepository.ID,
			WorktreeID:           previousRepository.Worktrees[0].ID,
			DisplayName:          "previous",
			DesiredState:         "unknown",
			ObservedState:        "unknown",
			Health:               "unknown",
			Services:             []contractv2.Service{},
			PortLeases:           []contractv2.PortLease{},
			InfrastructureLeases: []contractv2.InfrastructureLease{},
			URLs:                 map[string]string{},
			AttentionAlertIDs:    []string{},
		}},
		Operations: []contractv2.Operation{},
		Alerts: []contractv2.Alert{
			newInventoryAlert(now.Add(-time.Hour), "OLD_INVENTORY", "Old inventory.", "warning"),
			{
				ID: "alert_domain", Severity: "warning", Code: "DOMAIN_ALERT", Summary: "Domain alert.",
				Status: "active", FirstSeenAt: now, LastSeenAt: now, Occurrences: 1,
			},
		},
	}
	discoveredAlert := newInventoryAlert(now, "NEW_INVENTORY", "New inventory.", "warning")

	merged := mergeRepositoryInventory(snapshot, repositoryInventory{
		Repositories: []contractv2.Repository{discoveredRepository},
		Alerts:       []contractv2.Alert{discoveredAlert},
		Complete:     true,
	})
	if len(merged.Repositories) != 2 {
		t.Fatalf("repositories: got %+v", merged.Repositories)
	}
	if len(merged.Alerts) != 2 {
		t.Fatalf("alerts: got %+v", merged.Alerts)
	}
	if err := merged.Validate(); err != nil {
		t.Fatalf("merged snapshot is invalid: %v", err)
	}
}

func TestMergeRepositoryInventoryPreservesReferencedWorktreeInRefreshedRepository(t *testing.T) {
	previous := inventoryTestRepository("repo_test", "worktree_active", "/tmp/active")
	discovered := inventoryTestRepository("repo_test", "worktree_other", "/tmp/other")
	merged := mergeRepositoryInventory(contractv2.StatusSnapshot{
		Repositories: []contractv2.Repository{previous},
		Environments: []contractv2.Environment{{
			ID: "environment_active", RepositoryID: previous.ID, WorktreeID: previous.Worktrees[0].ID,
		}},
		Alerts: []contractv2.Alert{},
	}, repositoryInventory{
		Repositories: []contractv2.Repository{discovered}, Alerts: []contractv2.Alert{}, Complete: true,
	})
	if len(merged.Repositories) != 1 || len(merged.Repositories[0].Worktrees) != 2 ||
		!repositoryContainsWorktree(merged.Repositories[0], "worktree_active") {
		t.Fatalf("referenced worktree was discarded: %#v", merged.Repositories)
	}
}

func TestInventoryAlertsDeduplicateAndPreferErrors(t *testing.T) {
	now := time.Now().UTC()
	warning := newInventoryAlert(now, "SAME_CODE", "Warning.", "warning")
	failure := newInventoryAlert(now, "SAME_CODE", "Failure.", "error")
	alerts := deduplicateInventoryAlerts([]contractv2.Alert{warning, failure})
	if len(alerts) != 1 || alerts[0].Severity != "error" || alerts[0].Summary != "Failure." {
		t.Fatalf("deduplicated alerts: got %+v", alerts)
	}
}

func TestMergeRepositoryInventoryPreservesOnlyMatchingPullRequestObservation(t *testing.T) {
	observedAt := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	previous := inventoryTestRepository("repo_test", "worktree_test", "/tmp/repo")
	previous.Worktrees[0].Branch = "feature/test"
	previous.Worktrees[0].PullRequest = &contractv2.PullRequestObservation{
		Status: "none", ObservedAt: &observedAt, LastAttemptAt: observedAt,
	}
	discovered := inventoryTestRepository("repo_test", "worktree_test", "/tmp/repo")
	discovered.Worktrees[0].Branch = "feature/test"
	merged := mergeRepositoryInventory(contractv2.StatusSnapshot{
		Repositories: previousSlice(previous), Environments: []contractv2.Environment{}, Alerts: []contractv2.Alert{},
	}, repositoryInventory{Repositories: previousSlice(discovered), Complete: true})
	if merged.Repositories[0].Worktrees[0].PullRequest == nil {
		t.Fatal("matching observation was discarded")
	}

	discovered.Worktrees[0].HeadRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	merged = mergeRepositoryInventory(contractv2.StatusSnapshot{
		Repositories: previousSlice(previous), Environments: []contractv2.Environment{}, Alerts: []contractv2.Alert{},
	}, repositoryInventory{Repositories: previousSlice(discovered), Complete: true})
	if merged.Repositories[0].Worktrees[0].PullRequest != nil {
		t.Fatal("observation survived a changed local head")
	}
}

func previousSlice(repository contractv2.Repository) []contractv2.Repository {
	return []contractv2.Repository{repository}
}

func inventoryTestRepository(id, worktreeID, root string) contractv2.Repository {
	return contractv2.Repository{
		ID: id, DisplayName: id, RootPath: root, ProfileKey: "sample", Remote: "owner/repository",
		Worktrees: []contractv2.Worktree{{
			ID: worktreeID, Path: root, HeadRevision: "0123456789abcdef0123456789abcdef01234567",
			IsPrimary: true,
		}},
	}
}
