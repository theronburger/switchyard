package main

import (
	"context"
	"os"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

func TestMergeRepositoryInventoryPreservesRepositoriesWithEnvironments(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	previousRepository := inventoryTestRepository("repo_previous", "worktree_previous", "/tmp/previous")
	discoveredRepository := inventoryTestRepository("repo_discovered", "worktree_discovered", "/tmp/discovered")
	snapshot := contractv1.StatusSnapshot{
		SchemaVersion: 1,
		Daemon: contractv1.DaemonStatus{
			InstanceID: "daemon_test", Version: version, State: "ready", StartedAt: now,
		},
		Repositories: []contractv1.Repository{previousRepository},
		Environments: []contractv1.Environment{{
			ID:                   "env_previous",
			RepositoryID:         previousRepository.ID,
			WorktreeID:           previousRepository.Worktrees[0].ID,
			DisplayName:          "previous",
			Services:             []contractv1.Service{},
			PortLeases:           []contractv1.PortLease{},
			InfrastructureLeases: []contractv1.InfrastructureLease{},
			URLs:                 map[string]string{},
			AttentionAlertIDs:    []string{},
		}},
		Operations: []contractv1.Operation{},
		Alerts: []contractv1.Alert{
			newInventoryAlert(now.Add(-time.Hour), "OLD_INVENTORY", "Old inventory.", "warning"),
			{
				ID: "alert_domain", Severity: "warning", Code: "DOMAIN_ALERT", Summary: "Domain alert.",
				Status: "active", FirstSeenAt: now, LastSeenAt: now, Occurrences: 1,
			},
		},
	}
	discoveredAlert := newInventoryAlert(now, "NEW_INVENTORY", "New inventory.", "warning")

	merged := mergeRepositoryInventory(snapshot, repositoryInventory{
		Repositories: []contractv1.Repository{discoveredRepository},
		Alerts:       []contractv1.Alert{discoveredAlert},
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

func TestInventoryAlertsDeduplicateAndPreferErrors(t *testing.T) {
	now := time.Now().UTC()
	warning := newInventoryAlert(now, "SAME_CODE", "Warning.", "warning")
	failure := newInventoryAlert(now, "SAME_CODE", "Failure.", "error")
	alerts := deduplicateInventoryAlerts([]contractv1.Alert{warning, failure})
	if len(alerts) != 1 || alerts[0].Severity != "error" || alerts[0].Summary != "Failure." {
		t.Fatalf("deduplicated alerts: got %+v", alerts)
	}
}

func TestRealMarketplaceInventoryProbe(t *testing.T) {
	if os.Getenv("SWITCHYARD_TEST_REAL_MARKETPLACE") != "1" {
		t.Skip("set SWITCHYARD_TEST_REAL_MARKETPLACE=1 for the read-only local probe")
	}
	t.Setenv(repositoryRootOverride, "/Users/example/Developer/marketplace")
	t.Setenv(gitExecutableOverride, configuredGitExecutable())
	discovered := discoverRepositoryInventory(context.Background(), time.Now().UTC())
	if !discovered.Complete || len(discovered.Repositories) != 1 {
		t.Fatalf("Marketplace inventory is incomplete: %+v", discovered.Alerts)
	}
	if len(discovered.Repositories[0].Worktrees) < 1 {
		t.Fatal("Marketplace inventory contains no worktrees")
	}
}

func inventoryTestRepository(id, worktreeID, root string) contractv1.Repository {
	return contractv1.Repository{
		ID: id, DisplayName: id, RootPath: root, Adapter: "marketplace", Remote: "owner/repository",
		Worktrees: []contractv1.Worktree{{
			ID: worktreeID, Path: root, HeadRevision: "0123456789abcdef0123456789abcdef01234567",
			IsPrimary: true,
		}},
	}
}
