package statusview

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

func TestWorktreeByPathSelectsDeepestContainingWorktreeAndScopesState(t *testing.T) {
	snapshot := statusViewSnapshot()
	context, err := WorktreeByPath(snapshot, "/Developer/marketplace-worktrees/feature-a/services/api")
	if err != nil {
		t.Fatal(err)
	}
	if context.Worktree.ID != "worktree_linked" || context.Repository.ID != "repository_marketplace" {
		t.Fatalf("context: %+v", context)
	}
	if context.Repository.Observation == nil || context.Repository.Observation.Stale {
		t.Fatalf("repository observation was not projected: %+v", context.Repository.Observation)
	}
	if len(context.Environments) != 1 || context.Environments[0].ID != "environment_linked" ||
		len(context.Operations) != 2 || context.Operations[0].ID != "operation_new" ||
		len(context.Alerts) != 1 || context.Alerts[0].ID != "alert_linked" {
		t.Fatalf("scoped state: environments=%+v operations=%+v alerts=%+v", context.Environments, context.Operations, context.Alerts)
	}
	if _, err := WorktreeByPath(snapshot, "/Developer/marketplace-worktrees/feature-a-copy"); !errors.Is(err, ErrWorktreeNotFound) {
		t.Fatalf("path-prefix collision: %v", err)
	}
}

func TestWorktreeSelectorsAreExactAndAmbiguityIsRefused(t *testing.T) {
	snapshot := statusViewSnapshot()
	context, err := WorktreeBySelector(snapshot, "worktree_linked")
	if err != nil || context.Worktree.Branch != "feature/a" {
		t.Fatalf("ID selector: context=%+v error=%v", context, err)
	}
	context, err = WorktreeBySelector(snapshot, "feature/a")
	if err != nil || context.Worktree.ID != "worktree_linked" {
		t.Fatalf("branch selector: context=%+v error=%v", context, err)
	}
	snapshot.Repositories[0].Worktrees = append(snapshot.Repositories[0].Worktrees, contractv1.Worktree{
		ID: "worktree_duplicate", Path: "/Developer/other", Branch: "feature/a",
	})
	if _, err := WorktreeBySelector(snapshot, "feature/a"); !errors.Is(err, ErrWorktreeAmbiguous) {
		t.Fatalf("ambiguous selector: %v", err)
	}
	if _, err := WorktreeBySelector(snapshot, "feature"); !errors.Is(err, ErrWorktreeNotFound) {
		t.Fatalf("partial selector: %v", err)
	}
}

func TestEnvironmentByIDReturnsExactContext(t *testing.T) {
	status, err := EnvironmentByID(statusViewSnapshot(), "environment_linked")
	if err != nil {
		t.Fatal(err)
	}
	if status.Worktree.ID != "worktree_linked" || status.Environment.ID != "environment_linked" ||
		len(status.Operations) != 2 || len(status.Alerts) != 1 {
		t.Fatalf("environment status: %+v", status)
	}
	if _, err := EnvironmentByID(statusViewSnapshot(), "environment_missing"); !errors.Is(err, ErrEnvironmentNotFound) {
		t.Fatalf("missing environment: %v", err)
	}
}

func TestWorktreeByPathCanonicalizesExistingSymlink(t *testing.T) {
	root := t.TempDir()
	worktreePath := filepath.Join(root, "worktree")
	child := filepath.Join(worktreePath, "services", "api")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(worktreePath, alias); err != nil {
		t.Fatal(err)
	}
	snapshot := contractv1.StatusSnapshot{
		SchemaVersion: contractv1.SchemaVersion,
		Repositories: []contractv1.Repository{{
			ID: "repository_test", Worktrees: []contractv1.Worktree{{
				ID: "worktree_test", Path: worktreePath, Branch: "feature/test",
			}},
		}},
		Environments: []contractv1.Environment{}, Operations: []contractv1.Operation{}, Alerts: []contractv1.Alert{},
	}
	context, err := WorktreeByPath(snapshot, filepath.Join(alias, "services", "api"))
	if err != nil || context.Worktree.ID != "worktree_test" {
		t.Fatalf("symlink context=%+v error=%v", context, err)
	}
}

func statusViewSnapshot() contractv1.StatusSnapshot {
	now := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	return contractv1.StatusSnapshot{
		SchemaVersion: contractv1.SchemaVersion, SnapshotRevision: 42, GeneratedAt: now,
		Daemon: contractv1.DaemonStatus{InstanceID: "daemon_test", Version: "test", State: "ready", StartedAt: now},
		Repositories: []contractv1.Repository{{
			ID: "repository_marketplace", DisplayName: "marketplace", RootPath: "/Developer/marketplace",
			Adapter: "marketplace", Remote: "example/marketplace",
			Observation: &contractv1.RepositoryObservation{ObservedAt: &now, LastAttemptAt: now},
			Worktrees: []contractv1.Worktree{
				{ID: "worktree_primary", Path: "/Developer/marketplace", Branch: "main", IsPrimary: true},
				{ID: "worktree_linked", Path: "/Developer/marketplace-worktrees/feature-a", Branch: "feature/a"},
			},
		}},
		Environments: []contractv1.Environment{
			{ID: "environment_primary", RepositoryID: "repository_marketplace", WorktreeID: "worktree_primary", URLs: map[string]string{}, Services: []contractv1.Service{}, PortLeases: []contractv1.PortLease{}, InfrastructureLeases: []contractv1.InfrastructureLease{}, AttentionAlertIDs: []string{}},
			{ID: "environment_linked", RepositoryID: "repository_marketplace", WorktreeID: "worktree_linked", URLs: map[string]string{}, Services: []contractv1.Service{}, PortLeases: []contractv1.PortLease{}, InfrastructureLeases: []contractv1.InfrastructureLease{}, AttentionAlertIDs: []string{"alert_linked"}},
		},
		Operations: []contractv1.Operation{
			{ID: "operation_old", EnvironmentID: "environment_linked", UpdatedAt: now.Add(-time.Minute)},
			{ID: "operation_foreign", EnvironmentID: "environment_primary", UpdatedAt: now},
			{ID: "operation_new", EnvironmentID: "environment_linked", UpdatedAt: now},
		},
		Alerts: []contractv1.Alert{
			{ID: "alert_linked", EnvironmentID: "environment_linked", LastSeenAt: now},
			{ID: "alert_foreign", EnvironmentID: "environment_primary", LastSeenAt: now},
		},
	}
}
