package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupPlansOnlyStalePositivelyOwnedPreparation(t *testing.T) {
	runtimeRoot := t.TempDir()
	current := strings.Repeat("a", 64)
	stale := strings.Repeat("b", 64)
	foreign := strings.Repeat("c", 64)
	createOwnedPreparation(t, runtimeRoot, "sample", "worktree_01", current, "install")
	stalePath := createOwnedPreparation(t, runtimeRoot, "sample", "worktree_01", stale, "install")
	foreignPath := filepath.Join(runtimeRoot, "repositories", "sample", "worktree_01", "preparation", foreign, "unknown")
	if err := os.MkdirAll(foreignPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreignPath, "foreign.txt"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}

	planner := PrivatePreparationPlanner{RuntimeRoot: runtimeRoot, CurrentFingerprints: map[string]string{"worktree_01": current}}
	inventory, err := planner.Inventory(context.Background(), Scope{Kind: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Candidates) != 1 || inventory.Candidates[0].Path != stalePath {
		t.Fatalf("candidates: %+v", inventory.Candidates)
	}
	if len(inventory.Protected) != 2 {
		t.Fatalf("protections: %+v", inventory.Protected)
	}
	if err := planner.Remove(context.Background(), inventory.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale path remains: %v", err)
	}
	for _, protected := range []string{
		filepath.Join(runtimeRoot, "repositories", "sample", "worktree_01", "preparation", current),
		foreignPath,
	} {
		if _, err := os.Lstat(protected); err != nil {
			t.Fatalf("protected resource changed: %s: %v", protected, err)
		}
	}
}

func TestCleanupRefusesCandidateChangedAfterPlan(t *testing.T) {
	runtimeRoot := t.TempDir()
	fingerprint := strings.Repeat("d", 64)
	path := createOwnedPreparation(t, runtimeRoot, "sample", "worktree_01", fingerprint, "install")
	planner := PrivatePreparationPlanner{RuntimeRoot: runtimeRoot, CurrentFingerprints: map[string]string{}}
	inventory, err := planner.Inventory(context.Background(), Scope{Kind: "worktree", ID: "worktree_01"})
	if err != nil || len(inventory.Candidates) != 1 {
		t.Fatalf("inventory=%+v err=%v", inventory, err)
	}
	if err := os.WriteFile(filepath.Join(path, "install", "foreign.txt"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := planner.Remove(context.Background(), inventory.Candidates[0]); err == nil {
		t.Fatal("changed candidate was removed")
	}
	if _, err := os.Lstat(filepath.Join(path, "install", "foreign.txt")); err != nil {
		t.Fatalf("foreign file did not survive: %v", err)
	}
}

func TestCleanupScopeDoesNotCrossRepositories(t *testing.T) {
	runtimeRoot := t.TempDir()
	fingerprint := strings.Repeat("e", 64)
	first := createOwnedPreparation(t, runtimeRoot, "first", "worktree_01", fingerprint, "install")
	second := createOwnedPreparation(t, runtimeRoot, "second", "worktree_02", fingerprint, "install")
	planner := PrivatePreparationPlanner{RuntimeRoot: runtimeRoot, CurrentFingerprints: map[string]string{}}
	inventory, err := planner.Inventory(context.Background(), Scope{Kind: "repository", ID: "first"})
	if err != nil || len(inventory.Candidates) != 1 || inventory.Candidates[0].Path != first {
		t.Fatalf("inventory=%+v err=%v", inventory, err)
	}
	if err := planner.Remove(context.Background(), inventory.Candidates[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(second); err != nil {
		t.Fatalf("other repository changed: %v", err)
	}
}

func createOwnedPreparation(t *testing.T, root, profile, worktree, fingerprint, step string) string {
	t.Helper()
	directory := filepath.Join(root, "repositories", profile, worktree, "preparation", fingerprint, step)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := "{\"schemaVersion\":1,\"kind\":\"preparation-step\",\"stepId\":\"" + step + "\"}"
	if err := os.WriteFile(filepath.Join(directory, "ownership.json"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "stdout.log"), []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(directory)
}
