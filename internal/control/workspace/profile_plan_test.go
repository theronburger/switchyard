package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/theronburger/switchyard/internal/configuration"
)

func TestProfilePlanBuilderCompilesExactPrivatePlan(t *testing.T) {
	worktree := t.TempDir()
	runtimeRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "lockfile"), []byte("locked"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewProfilePlanBuilder([]ProfileRegistration{{
		WorktreeID: "worktree_01", WorktreeRoot: worktree, ProfileKey: "sample",
		ProfileDigest: "sha256:test", RuntimeRoot: runtimeRoot, Ownership: OwnershipAdopted,
		Preparation: configuration.Preparation{
			Fingerprint: configuration.Fingerprint{Files: []string{"lockfile"}},
			Steps: []configuration.PreparationStep{{
				ID: "install", Executable: executable, Arguments: []string{"helper"}, WorkingDirectory: ".",
				Environment: map[string]string{"CACHE_MODE": "shared"}, Timeout: "1m",
			}},
			Verify: []configuration.Verification{{ID: "lock", Kind: "regular-file", Path: "lockfile"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := builder.Build(PlanningRequest{OperationID: "operation_01", WorktreeID: "worktree_01"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Adapter != "sample" || len(plan.Steps) != 1 || len(plan.Requirements) != 1 ||
		plan.Steps[0].Executable != executable || plan.Steps[0].Directory != worktree ||
		plan.Steps[0].RunDirectory == worktree {
		t.Fatalf("plan: %+v", plan)
	}
}

func TestProfileFingerprintRejectsSymlinks(t *testing.T) {
	worktree := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(worktree, "lockfile")); err != nil {
		t.Fatal(err)
	}
	_, err := profileFingerprint(ProfileRegistration{
		WorktreeID: "worktree_01", WorktreeRoot: worktree, ProfileDigest: "sha256:test",
		Preparation: configuration.Preparation{Fingerprint: configuration.Fingerprint{Files: []string{"lockfile"}}},
	})
	if err == nil {
		t.Fatal("symlinked fingerprint input was accepted")
	}
}
