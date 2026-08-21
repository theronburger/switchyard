package statusview

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

func TestWorktreeByPathSelectsDeepestContainingWorktreeAndScopesState(t *testing.T) {
	snapshot := statusViewSnapshot()
	context, err := WorktreeByPath(snapshot, "/Developer/sample-worktrees/feature-a/services/api")
	if err != nil {
		t.Fatal(err)
	}
	if context.Worktree.ID != "worktree_linked" || context.Repository.ID != "repository_sample" {
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
	if _, err := WorktreeByPath(snapshot, "/Developer/sample-worktrees/feature-a-copy"); !errors.Is(err, ErrWorktreeNotFound) {
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
	snapshot.Repositories[0].Worktrees = append(snapshot.Repositories[0].Worktrees, contractv2.Worktree{
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
	snapshot := contractv2.StatusSnapshot{
		SchemaVersion: contractv2.SchemaVersion,
		Repositories: []contractv2.Repository{{
			ID: "repository_test", Worktrees: []contractv2.Worktree{{
				ID: "worktree_test", Path: worktreePath, Branch: "feature/test",
			}},
		}},
		Environments: []contractv2.Environment{}, Operations: []contractv2.Operation{}, Alerts: []contractv2.Alert{},
	}
	context, err := WorktreeByPath(snapshot, filepath.Join(alias, "services", "api"))
	if err != nil || context.Worktree.ID != "worktree_test" {
		t.Fatalf("symlink context=%+v error=%v", context, err)
	}
}

func statusViewSnapshot() contractv2.StatusSnapshot {
	now := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	return contractv2.StatusSnapshot{
		SchemaVersion: contractv2.SchemaVersion, SnapshotRevision: 42, GeneratedAt: now,
		Daemon: contractv2.DaemonStatus{InstanceID: "daemon_test", Version: "test", State: "ready", StartedAt: now},
		Repositories: []contractv2.Repository{{
			ID: "repository_sample", DisplayName: "sample", RootPath: "/Developer/sample",
			ProfileKey: "sample", Remote: "example/sample",
			Observation: &contractv2.RepositoryObservation{ObservedAt: &now, LastAttemptAt: now},
			Worktrees: []contractv2.Worktree{
				{ID: "worktree_primary", Path: "/Developer/sample", Branch: "main", IsPrimary: true},
				{ID: "worktree_linked", Path: "/Developer/sample-worktrees/feature-a", Branch: "feature/a"},
			},
		}},
		Environments: []contractv2.Environment{
			{ID: "environment_primary", RepositoryID: "repository_sample", WorktreeID: "worktree_primary", URLs: map[string]string{}, Services: []contractv2.Service{}, PortLeases: []contractv2.PortLease{}, InfrastructureLeases: []contractv2.InfrastructureLease{}, AttentionAlertIDs: []string{}},
			{ID: "environment_linked", RepositoryID: "repository_sample", WorktreeID: "worktree_linked", URLs: map[string]string{}, Services: []contractv2.Service{}, PortLeases: []contractv2.PortLease{}, InfrastructureLeases: []contractv2.InfrastructureLease{}, AttentionAlertIDs: []string{"alert_linked"}},
		},
		Operations: []contractv2.Operation{
			{ID: "operation_old", EnvironmentID: "environment_linked", UpdatedAt: now.Add(-time.Minute)},
			{ID: "operation_foreign", EnvironmentID: "environment_primary", UpdatedAt: now},
			{ID: "operation_new", EnvironmentID: "environment_linked", UpdatedAt: now},
		},
		Alerts: []contractv2.Alert{
			{ID: "alert_linked", EnvironmentID: "environment_linked", LastSeenAt: now},
			{ID: "alert_foreign", EnvironmentID: "environment_primary", LastSeenAt: now},
		},
	}
}
