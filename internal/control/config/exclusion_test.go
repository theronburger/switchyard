package config

import (
	"testing"

	"github.com/theronburger/switchyard/internal/control/inventory"
)

func TestPlanLocalManifestExclusionTargetsOnlySharedCommonExclude(t *testing.T) {
	plan, err := PlanLocalManifestExclusion(inventory.RepositoryControlPaths{
		CommonDirectory:   "/Repositories/marketplace/.git",
		SharedExcludePath: "/Repositories/marketplace/.git/info/exclude",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SharedExcludePath != "/Repositories/marketplace/.git/info/exclude" ||
		plan.Pattern != "/.switchyard.yaml" {
		t.Fatalf("exclusion plan: %#v", plan)
	}
}

func TestPlanLocalManifestExclusionRefusesPublicGitignore(t *testing.T) {
	_, err := PlanLocalManifestExclusion(inventory.RepositoryControlPaths{
		CommonDirectory:   "/Repositories/marketplace/.git",
		SharedExcludePath: "/Repositories/marketplace/.gitignore",
	})
	if err == nil {
		t.Fatal("expected public gitignore target to fail")
	}
}
