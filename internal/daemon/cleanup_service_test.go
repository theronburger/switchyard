package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/state"
)

type cleanupWorkspaceSource struct {
	results []workspacecontrol.Result
}

func (source cleanupWorkspaceSource) ListCurrent(context.Context) ([]workspacecontrol.Result, error) {
	return source.results, nil
}

func TestCleanupServicePlansAppliesAndConsumesExactRevision(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC)
	runtimeRoot := t.TempDir()
	fingerprint := strings.Repeat("a", 64)
	path := cleanupServicePreparation(t, runtimeRoot, "profile", "worktree_01", fingerprint)
	store, err := state.Open(ctx, state.Config{Path: filepath.Join(t.TempDir(), "state.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	service := CleanupService{
		Store: store, Workspaces: cleanupWorkspaceSource{}, RuntimeRoot: runtimeRoot,
		Now: func() time.Time { return now }, NewID: func() (string, error) { return "cleanup_plan_01", nil },
	}
	plan, err := service.Plan(ctx, contractv1.CleanupPlanRequest{
		SchemaVersion: contractv1.SchemaVersion, Scope: contractv1.CleanupScope{Kind: "global"},
	})
	if err != nil || plan.Validate() != nil || len(plan.Candidates) != 1 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if _, err := service.Apply(ctx, contractv1.CleanupApplyRequest{
		SchemaVersion: contractv1.SchemaVersion, PlanID: plan.ID,
		ExpectedRevision: plan.Revision + 1, CandidateIDs: []string{plan.Candidates[0].ID},
	}); !errors.Is(err, state.ErrCleanupPlanNotFound) {
		t.Fatalf("stale revision: %v", err)
	}
	result, err := service.Apply(ctx, contractv1.CleanupApplyRequest{
		SchemaVersion: contractv1.SchemaVersion, PlanID: plan.ID,
		ExpectedRevision: plan.Revision, CandidateIDs: []string{plan.Candidates[0].ID},
	})
	if err != nil || result.Validate() != nil || !result.Removals[0].Removed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("owned candidate remains: %v", err)
	}
	if _, err := service.Apply(ctx, contractv1.CleanupApplyRequest{
		SchemaVersion: contractv1.SchemaVersion, PlanID: plan.ID,
		ExpectedRevision: plan.Revision, CandidateIDs: []string{},
	}); !errors.Is(err, state.ErrCleanupPlanConsumed) {
		t.Fatalf("reused plan: %v", err)
	}
}

func TestCleanupServiceRefusesCandidateModifiedAfterReview(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC)
	runtimeRoot := t.TempDir()
	path := cleanupServicePreparation(t, runtimeRoot, "profile", "worktree_01", strings.Repeat("b", 64))
	store, err := state.Open(ctx, state.Config{Path: filepath.Join(t.TempDir(), "state.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	service := CleanupService{Store: store, Workspaces: cleanupWorkspaceSource{}, RuntimeRoot: runtimeRoot, Now: func() time.Time { return now }, NewID: func() (string, error) { return "cleanup_plan_changed", nil }}
	plan, err := service.Plan(ctx, contractv1.CleanupPlanRequest{SchemaVersion: contractv1.SchemaVersion, Scope: contractv1.CleanupScope{Kind: "global"}})
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(path, "install", "foreign.txt")
	if err := os.WriteFile(foreign, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.Apply(ctx, contractv1.CleanupApplyRequest{
		SchemaVersion: contractv1.SchemaVersion, PlanID: plan.ID,
		ExpectedRevision: plan.Revision, CandidateIDs: []string{plan.Candidates[0].ID},
	})
	if err != nil || result.Removals[0].Removed || result.Removals[0].Reason != "changed-or-protected" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if contents, err := os.ReadFile(foreign); err != nil || string(contents) != "preserve" {
		t.Fatalf("foreign resource changed: %q %v", contents, err)
	}
}

func cleanupServicePreparation(t *testing.T, root, profile, worktree, fingerprint string) string {
	t.Helper()
	path := filepath.Join(root, "repositories", profile, worktree, "preparation", fingerprint, "install")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "ownership.json"), []byte(`{"schemaVersion":1,"kind":"preparation-step","stepId":"install"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "stdout.log"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(path)
}
