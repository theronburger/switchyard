package main

import (
	"context"
	"os"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	controlconfig "github.com/theronburger/switchyard/internal/control/config"
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
			DesiredState:         "unknown",
			ObservedState:        "unknown",
			Health:               "unknown",
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

func TestMergeRepositoryInventoryPreservesReferencedWorktreeInRefreshedRepository(t *testing.T) {
	previous := inventoryTestRepository("repo_test", "worktree_active", "/tmp/active")
	discovered := inventoryTestRepository("repo_test", "worktree_other", "/tmp/other")
	merged := mergeRepositoryInventory(contractv1.StatusSnapshot{
		Repositories: []contractv1.Repository{previous},
		Environments: []contractv1.Environment{{
			ID: "environment_active", RepositoryID: previous.ID, WorktreeID: previous.Worktrees[0].ID,
		}},
		Alerts: []contractv1.Alert{},
	}, repositoryInventory{
		Repositories: []contractv1.Repository{discovered}, Alerts: []contractv1.Alert{}, Complete: true,
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
	alerts := deduplicateInventoryAlerts([]contractv1.Alert{warning, failure})
	if len(alerts) != 1 || alerts[0].Severity != "error" || alerts[0].Summary != "Failure." {
		t.Fatalf("deduplicated alerts: got %+v", alerts)
	}
}

func TestMergeRepositoryInventoryPreservesOnlyMatchingPullRequestObservation(t *testing.T) {
	observedAt := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	previous := inventoryTestRepository("repo_test", "worktree_test", "/tmp/repo")
	previous.Worktrees[0].Branch = "feature/test"
	previous.Worktrees[0].PullRequest = &contractv1.PullRequestObservation{
		Status: "none", ObservedAt: &observedAt, LastAttemptAt: observedAt,
	}
	discovered := inventoryTestRepository("repo_test", "worktree_test", "/tmp/repo")
	discovered.Worktrees[0].Branch = "feature/test"
	merged := mergeRepositoryInventory(contractv1.StatusSnapshot{
		Repositories: previousSlice(previous), Environments: []contractv1.Environment{}, Alerts: []contractv1.Alert{},
	}, repositoryInventory{Repositories: previousSlice(discovered), Complete: true})
	if merged.Repositories[0].Worktrees[0].PullRequest == nil {
		t.Fatal("matching observation was discarded")
	}

	discovered.Worktrees[0].HeadRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	merged = mergeRepositoryInventory(contractv1.StatusSnapshot{
		Repositories: previousSlice(previous), Environments: []contractv1.Environment{}, Alerts: []contractv1.Alert{},
	}, repositoryInventory{Repositories: previousSlice(discovered), Complete: true})
	if merged.Repositories[0].Worktrees[0].PullRequest != nil {
		t.Fatal("observation survived a changed local head")
	}
}

func previousSlice(repository contractv1.Repository) []contractv1.Repository {
	return []contractv1.Repository{repository}
}

func TestMarketplaceRuntimeCatalogUsesLocalOrderingAndCompleteAvailability(t *testing.T) {
	runtime, err := marketplaceRuntimeCatalog(controlconfig.RuntimeSettings{
		DefaultTarget:      "testing",
		Targets:            []string{"testing", "production"},
		WarnOnStartTargets: []string{"production"},
		Services:           []string{"organizer", "auth-service", "app"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.DefaultTargetID != "testing" || len(runtime.Targets) != 2 ||
		runtime.Targets[0].WarnOnStart || runtime.Targets[1].ID != "production" ||
		!runtime.Targets[1].WarnOnStart || runtime.Targets[1].Risk != "production" {
		t.Fatalf("runtime targets: %+v", runtime)
	}
	if len(runtime.Services) != 3 || !runtime.Services[0].Available ||
		!runtime.Services[1].Available || runtime.Services[1].UnavailableReason != "" ||
		!runtime.Services[2].Available {
		t.Fatalf("runtime services: %+v", runtime.Services)
	}
}

func TestMarketplaceRuntimeCatalogRejectsUnknownLocalEntries(t *testing.T) {
	_, err := marketplaceRuntimeCatalog(controlconfig.RuntimeSettings{
		DefaultTarget: "testing", Targets: []string{"testing"}, Services: []string{"made-up-service"},
	})
	if err == nil {
		t.Fatal("unknown local service was accepted")
	}
}

func TestMarketplaceRuntimeCatalogAllowsExplicitEmptyWarningOverride(t *testing.T) {
	runtime, err := marketplaceRuntimeCatalog(controlconfig.RuntimeSettings{
		DefaultTarget:      "testing",
		Targets:            []string{"testing", "production"},
		WarnOnStartTargets: []string{},
		Services:           []string{"organizer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Targets[1].WarnOnStart {
		t.Fatal("explicit empty warn-on-start override was ignored")
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

func TestDevelopmentInventoryDoesNotDiscoverARealRepositoryByDefault(t *testing.T) {
	t.Setenv(repositoryRootOverride, "")
	discovered := discoverRepositoryInventory(context.Background(), time.Now().UTC())
	if !discovered.Complete || len(discovered.Repositories) != 0 || len(discovered.Alerts) != 0 {
		t.Fatalf("development inventory was not isolated: %+v", discovered)
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
