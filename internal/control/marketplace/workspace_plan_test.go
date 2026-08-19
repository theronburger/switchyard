package marketplacecontrol

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/theronburger/switchyard/internal/control/workspace"
)

func TestWorkspacePlanBuilderProducesExactHydrationPlanAndStableFingerprint(t *testing.T) {
	t.Parallel()
	registration := marketplaceWorkspaceRegistration(t)
	builder, err := NewWorkspacePlanBuilder([]WorkspaceRegistration{registration})
	if err != nil {
		t.Fatal(err)
	}
	request := workspace.PlanningRequest{OperationID: "operation_01", WorktreeID: registration.WorktreeID}
	first, err := builder.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := builder.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || len(first.Steps) != 1 {
		t.Fatalf("plan was not deterministic: first=%+v second=%+v", first, second)
	}
	step := first.Steps[0]
	wantArguments := []string{registration.YarnCJS, "install", "--immutable"}
	if step.Executable != registration.NodeExecutable || !reflect.DeepEqual(step.Arguments, wantArguments) ||
		step.Directory != registration.WorktreeRoot || first.Ownership != workspace.OwnershipAdopted {
		t.Fatalf("hydration step: %+v", step)
	}
	if first.Toolchains[0].ID != "node" || first.Toolchains[0].RequestedVersion != "24" ||
		first.Toolchains[0].ResolvedVersion != "24.19.0" {
		t.Fatalf("toolchain metadata: %+v", first.Toolchains)
	}

	if err := os.MkdirAll(filepath.Join(registration.WorktreeRoot, "node_modules", "ignored"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registration.WorktreeRoot, "node_modules", "ignored", "package.json"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	ignored, err := builder.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if ignored.Fingerprint != first.Fingerprint {
		t.Fatal("generated node_modules content changed the workspace fingerprint")
	}

	if err := os.WriteFile(filepath.Join(registration.WorktreeRoot, "package.json"), []byte("{\"changed\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := builder.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Fingerprint == first.Fingerprint {
		t.Fatal("repository manifest change did not invalidate the workspace fingerprint")
	}
}

func TestWorkspacePlanBuilderRejectsSymlinkedManifest(t *testing.T) {
	t.Parallel()
	registration := marketplaceWorkspaceRegistration(t)
	foreign := filepath.Join(t.TempDir(), "foreign-package.json")
	if err := os.WriteFile(foreign, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(registration.WorktreeRoot, "package.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(registration.WorktreeRoot, "package.json")); err != nil {
		t.Fatal(err)
	}
	builder, err := NewWorkspacePlanBuilder([]WorkspaceRegistration{registration})
	if err != nil {
		t.Fatal(err)
	}
	_, err = builder.Build(workspace.PlanningRequest{
		OperationID: "operation_01", WorktreeID: registration.WorktreeID,
	})
	if err == nil {
		t.Fatal("symlinked repository manifest was accepted")
	}
}

func marketplaceWorkspaceRegistration(t *testing.T) WorkspaceRegistration {
	t.Helper()
	root := t.TempDir()
	for name, contents := range map[string]string{
		".nvmrc": "24\n", ".yarnrc.yml": "yarnPath: .yarn/releases/yarn.cjs\n",
		"yarn.lock": "lock\n", "package.json": "{}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	yarn := filepath.Join(root, ".yarn", "releases", "yarn.cjs")
	if err := os.MkdirAll(filepath.Dir(yarn), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(yarn, []byte("// yarn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(node, []byte("node\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return WorkspaceRegistration{
		WorktreeID: "worktree_01", WorktreeRoot: root, NodeExecutable: node,
		NodeRequested: "24", NodeResolved: "24.19.0", YarnCJS: yarn,
		RunRoot: filepath.Join(t.TempDir(), "runtime"), HomeDirectory: t.TempDir(),
		TemporaryDirectory: t.TempDir(), ExecutablePath: filepath.Dir(node) + ":/usr/bin:/bin",
		Ownership: workspace.OwnershipAdopted,
	}
}
