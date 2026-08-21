package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/state"
)

// A value source that is missing or invalid in one worktree is that
// worktree's problem. Boot must still complete for every other repository
// and worktree, the failing worktree must refuse new starts with a bounded
// error, and the error must never carry the bytes that were read.
func TestBootSurvivesMissingValueSourceInOneWorktree(t *testing.T) {
	t.Setenv(gitExecutableOverride, "/usr/bin/false")
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := applicationPaths{
		root: filepath.Join(base, "Switchyard"), directory: filepath.Join(base, "Switchyard", "daemon"),
		database:      filepath.Join(base, "Switchyard", "daemon", "state-v2.sqlite"),
		configuration: filepath.Join(base, "Switchyard", "configuration.yaml"),
	}
	if err := os.MkdirAll(paths.directory, 0o700); err != nil {
		t.Fatal(err)
	}
	healthyRoot := filepath.Join(base, "healthy")
	brokenRoot := filepath.Join(base, "broken")
	for _, root := range []string{healthyRoot, brokenRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(healthyRoot, "VERSION"), []byte("1.2.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The broken worktree holds an invalid structured source whose bytes
	// must never surface; the healthy one resolves normally.
	if err := os.WriteFile(filepath.Join(brokenRoot, "VERSION"), []byte("sentinel-unreadable-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	profileFor := func(root, kind string) configuration.Repository {
		return configuration.Repository{
			Enabled: true, DisplayName: "Sample", Root: root,
			Git:    configuration.Git{Remote: "origin", DefaultBase: "origin/main", ManagedWorktreesRoot: root + "-worktrees"},
			Values: map[string]configuration.ValueSource{"release": {Kind: kind, Root: "worktree", Path: "VERSION", Key: keyFor(kind)}},
			Targets: map[string]configuration.Target{"local": {DisplayName: "Local", Risk: "local",
				Environment: map[string]configuration.ValueRef{"RELEASE": {Value: "release"}}}},
			DefaultTarget: "local",
			Services: map[string]configuration.Service{"web": {
				DisplayName: "Web", Kind: "web", Ports: map[string]configuration.Port{},
				Environment: map[string]configuration.ValueRef{},
				Command:     configuration.Command{Executable: "/usr/bin/true", WorkingDirectory: ".", Timeout: "30s", Environment: map[string]configuration.ValueRef{}},
			}},
		}
	}
	discovered := repositoryInventory{
		Repositories: []contractv2.Repository{
			{ID: "repository_healthy", RootPath: healthyRoot, Worktrees: []contractv2.Worktree{{ID: "worktree_healthy", Path: healthyRoot, IsPrimary: true}}},
			{ID: "repository_broken", RootPath: brokenRoot, Worktrees: []contractv2.Worktree{{ID: "worktree_broken", Path: brokenRoot, IsPrimary: true}}},
		},
		Profiles: map[string]configuration.Repository{
			"repository_healthy": profileFor(healthyRoot, "text-file"),
			"repository_broken":  profileFor(brokenRoot, "json-pointer"),
		},
		ProfileKeys:    map[string]string{"repository_healthy": "healthy", "repository_broken": "broken"},
		ProfileDigests: map[string]string{"repository_healthy": "sha256:" + strings.Repeat("a", 64), "repository_broken": "sha256:" + strings.Repeat("b", 64)},
		FirstPort:      40000, LastPort: 40100, Complete: true,
	}

	store, err := state.Open(context.Background(), state.Config{Path: paths.database})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime, err := buildConfiguredProfileRuntime(ctx, store, paths, "daemon_01", discovered, func() {})
	if err != nil {
		t.Fatalf("a broken value source in one worktree aborted daemon boot: %v", err)
	}
	t.Cleanup(func() { _ = runtime.CloseAndWait(context.Background()) })
	if runtime.actions == nil {
		t.Fatal("the healthy repository was not registered")
	}

	values, code := readConfiguredValues(discovered.Profiles["repository_broken"], brokenRoot, brokenRoot)
	if code != worktreeValueSourceUnavailableCode || len(values) != 0 {
		t.Fatalf("broken value source: values=%v code=%q", values, code)
	}
	values, code = readConfiguredValues(discovered.Profiles["repository_healthy"], healthyRoot, healthyRoot)
	if code != "" || values["release"] != "1.2.3\n" {
		t.Fatalf("healthy value source: values=%v code=%q", values, code)
	}
}

func keyFor(kind string) string {
	if kind == "text-file" {
		return ""
	}
	return "/version"
}

// The unavailable rule itself, isolated from the configuration fail-closed
// gate that precedes it in ResolveStart.
func TestUnavailableWorktreeRefusesStartWithBoundedError(t *testing.T) {
	base := t.TempDir()
	configurationPath := writeAcceptedConfigurationForResolver(t, base)
	loaded, err := configuration.LoadFile(configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	worktreeRoot := filepath.Join(base, "repository")
	resolver := newConfiguredActionResolver([]configuredEnvironment{
		{EnvironmentID: "environment_broken", RepositoryID: "repository_broken", Worktree: contractv2.Worktree{ID: "worktree_broken", Path: worktreeRoot},
			ProfileKey: "sample", ProfileDigest: loaded.Digest, Profile: loaded.Document.Repositories["sample"],
			UnavailableCode: worktreeValueSourceUnavailableCode},
	}, configurationPath, loaded.Digest)
	_, err = resolver.ResolveStart(context.Background(), contractv2.StartEnvironmentRequest{WorktreeID: "worktree_broken", ServiceIDs: []string{"web"}})
	var actionError *daemon.ActionError
	if !errors.As(err, &actionError) || actionError.Contract.Code != worktreeValueSourceUnavailableCode || actionError.Status != 409 {
		t.Fatalf("unavailable worktree start: %v", err)
	}
	if strings.Contains(actionError.Contract.Message, "sentinel") || strings.Contains(actionError.Contract.Message, worktreeRoot) {
		t.Fatalf("bounded error leaked worktree detail: %q", actionError.Contract.Message)
	}
}

func writeAcceptedConfigurationForResolver(t *testing.T, base string) string {
	t.Helper()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "repository")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := `schemaVersion: 1
machine:
  ports: {first: 40000, last: 40100}
  execution: {inheritedEnvironment: [], shellDefault: deny}
repositories:
  sample:
    enabled: true
    displayName: Sample
    root: ` + root + `
    git: {remote: origin, defaultBase: origin/main, managedWorktreesRoot: ` + root + `-worktrees}
    targets:
      local: {displayName: Local, risk: local, environment: {}}
    defaultTarget: local
    services:
      web:
        displayName: Web
        kind: web
        command: {executable: /usr/bin/true, workingDirectory: ., timeout: 30s}
`
	path := filepath.Join(base, "configuration.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
