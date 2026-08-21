package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

// The daemon's runners write process evidence, step and action run
// directories, and managed-worktree ownership under one runtime tree, and
// cleanup planning and operation diagnostics read the same tree. A
// regression in either direction would make cleanup blind to private
// preparation directories and diagnostics unable to resolve log references.
func TestRuntimeRootIsSharedByWritersReadersAndPlanners(t *testing.T) {
	t.Setenv(gitExecutableOverride, "/usr/bin/false")
	base := t.TempDir()
	paths := applicationPaths{
		root:          filepath.Join(base, "Switchyard"),
		directory:     filepath.Join(base, "Switchyard", "daemon"),
		database:      filepath.Join(base, "Switchyard", "daemon", "state-v2.sqlite"),
		configuration: filepath.Join(base, "Switchyard", "configuration.yaml"),
	}
	if err := os.MkdirAll(paths.directory, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := paths.runtimeRoot()
	if !strings.HasPrefix(runtimeRoot, paths.root+string(filepath.Separator)) {
		t.Fatalf("runtime root %q is not beneath the application root %q", runtimeRoot, paths.root)
	}

	store, err := state.Open(context.Background(), state.Config{Path: paths.database})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Writers: the profile runtime creates and narrows the runtime tree the
	// step, initialization, and action runners are registered with.
	runtime, err := buildConfiguredProfileRuntime(ctx, store, paths, "daemon_01", repositoryInventory{}, func() {})
	if err != nil {
		t.Fatalf("build runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.CloseAndWait(context.Background()) })
	info, err := os.Stat(runtimeRoot)
	if err != nil {
		t.Fatalf("runtime root was not created by the runners: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime root mode %v, want 0700", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(paths.directory, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a second runtime tree exists under the daemon directory: %v", err)
	}

	// Managed-worktree ownership records live in the same tree.
	manager, err := newManagedWorkspaceManager(paths, repositoryInventory{
		Repositories: []contractv2.Repository{{ID: "repository_01", RootPath: filepath.Join(base, "repository")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := manager.OwnershipRoot(), filepath.Join(runtimeRoot, "managed-workspaces"); got != want {
		t.Fatalf("managed ownership root %q, want %q", got, want)
	}

	// Readers and planners: cleanup planning and operation diagnostics
	// resolve against exactly the tree the writers populate.
	journal, err := state.NewWorkspaceJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	if got := newCleanupService(store, journal, paths).RuntimeRoot; got != runtimeRoot {
		t.Fatalf("cleanup planner root %q, want %q", got, runtimeRoot)
	}
	diagnostics, err := newOperationDiagnosticsReader(store, paths)
	if err != nil {
		t.Fatal(err)
	}
	if got := diagnostics.RuntimeRoot(); got != runtimeRoot {
		t.Fatalf("diagnostics root %q, want %q", got, runtimeRoot)
	}
}
