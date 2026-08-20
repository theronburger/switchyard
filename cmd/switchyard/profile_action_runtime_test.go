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
	profilecontrol "github.com/theronburger/switchyard/internal/control/profile"
	"github.com/theronburger/switchyard/internal/daemon"
)

type actionSnapshotSource struct {
	snapshot contractv2.StatusSnapshot
	err      error
}

func (source actionSnapshotSource) ReadSnapshot(context.Context) (contractv2.StatusSnapshot, error) {
	return source.snapshot, source.err
}

func actionTestRegistrations(t *testing.T) (repositoryInventory, []profilecontrol.Registration, string) {
	t.Helper()
	root := t.TempDir()
	repositoryRoot := filepath.Join(root, "repo")
	linkedRoot := filepath.Join(root, "linked")
	runtimeRoot := filepath.Join(root, "runtime")
	for _, directory := range []string{repositoryRoot, linkedRoot, runtimeRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configurationDirectory := filepath.Join(root, "configuration")
	if err := os.Mkdir(configurationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	configurationPath := filepath.Join(configurationDirectory, "configuration.yaml")
	document := `schemaVersion: 1
machine:
  ports: {first: 30000, last: 49999}
  execution: {inheritedEnvironment: [], shellDefault: deny}
secretProviders: {}
repositories:
  sample:
    enabled: true
    displayName: Sample
    root: ` + repositoryRoot + `
    git: {remote: origin, defaultBase: origin/main, managedWorktreesRoot: ` + filepath.Join(root, "worktrees") + `}
    values: {}
    toolchains: {}
    caches: {}
    environmentSources: {}
    preparation: {}
    targets:
      local: {}
    defaultTarget: local
    services:
      web:
        displayName: Web
        kind: web
        ports:
          http: {}
        command: {executable: /usr/bin/true, arguments: [], workingDirectory: ., environment: {}, timeout: 30s}
    infrastructure: {}
    artifacts: {}
    actions:
      tidy:
        displayName: Tidy
        scope: worktree
        risk: local
        command: {executable: /usr/bin/true, arguments: [{literal: tidy}], workingDirectory: ., environment: {}, timeout: 1m}
      probe:
        displayName: Probe
        scope: service
        risk: remote-read
        command: {executable: /usr/bin/true, arguments: [{url: {purpose: http, scheme: http, host: localhost, path: /health}}], workingDirectory: ., environment: {}, timeout: 1m}
      audit:
        displayName: Audit
        scope: repository
        risk: remote-write
        command: {executable: /usr/bin/true, arguments: [], workingDirectory: ., environment: {}, timeout: 1m}
      up:
        displayName: Start
        scope: worktree
        risk: local
        lifecycle: start
    cleanup: {}
`
	if err := os.WriteFile(configurationPath, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := configuration.LoadFile(configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	profile := loaded.Document.Repositories["sample"]
	digest := "sha256:" + strings.Repeat("b", 64)
	registration := func(environmentID, worktreeID, worktreeRoot string) profilecontrol.Registration {
		return profilecontrol.Registration{
			EnvironmentID: environmentID, RepositoryID: "repository_01", WorktreeID: worktreeID,
			ProfileKey: "sample", ProfileDigest: digest, RepositoryRoot: repositoryRoot, WorktreeRoot: worktreeRoot,
			RuntimeRoot: runtimeRoot, CacheRoot: filepath.Join(runtimeRoot, "caches"),
			HomeDirectory: filepath.Join(runtimeRoot, "home"), HostHomeDirectory: root, TemporaryDirectory: filepath.Join(runtimeRoot, "tmp"),
			ExecutablePath: "/usr/bin:/bin", DaemonInstanceID: "daemon_01", Values: map[string]string{}, Profile: profile,
		}
	}
	registrations := []profilecontrol.Registration{
		registration("environment_linked", "worktree_linked", linkedRoot),
		registration("environment_primary", "worktree_primary", repositoryRoot),
	}
	inventory := repositoryInventory{AcceptedConfigurationDigest: loaded.Digest}
	return inventory, registrations, configurationPath
}

func TestConfiguredProfileActionResolverListsAndPinsAcceptedActions(t *testing.T) {
	inventory, registrations, configurationPath := actionTestRegistrations(t)
	resolver, err := newConfiguredProfileActionResolver(inventory, registrations, actionSnapshotSource{}, configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	list, err := resolver.ListActions(context.Background())
	if err != nil || list.AcceptedDigest != inventory.AcceptedConfigurationDigest || len(list.Actions) != 4 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	if list.Actions[0].ID != "audit" || !list.Actions[0].RequiresConfirmation || list.Actions[3].Kind != "lifecycle" {
		t.Fatalf("list ordering or projection: %+v", list.Actions)
	}
	if resolver.repositories["repository_01"].Primary.WorktreeID != "worktree_primary" {
		t.Fatal("repository-scoped actions must compile against the checkout at the repository root")
	}
	request := contractv2.RunProfileActionRequest{RepositoryID: "repository_01", ActionID: "tidy", WorktreeID: "worktree_linked"}
	resolution, err := resolver.ResolveAction(context.Background(), request)
	if err != nil || resolution.Definition.ID != "tidy" || resolution.AcceptedDigest != inventory.AcceptedConfigurationDigest ||
		resolution.Target.WorktreeID != "worktree_linked" {
		t.Fatalf("resolution: %+v err=%v", resolution, err)
	}
	command, err := resolver.CompileAction(context.Background(), resolution, "operation_01")
	if err != nil || command.Directory != registrations[0].WorktreeRoot || strings.Join(command.Arguments, " ") != "tidy" {
		t.Fatalf("compiled: %+v err=%v", command, err)
	}
	start, err := resolver.ResolveAction(context.Background(), contractv2.RunProfileActionRequest{RepositoryID: "repository_01", ActionID: "up", WorktreeID: "worktree_linked"})
	if err != nil || strings.Join(start.StartServiceIDs, ",") != "web" {
		t.Fatalf("start resolution: %+v err=%v", start, err)
	}
}

func TestConfiguredProfileActionResolverFailsClosedAndValidatesTargets(t *testing.T) {
	inventory, registrations, configurationPath := actionTestRegistrations(t)
	resolver, err := newConfiguredProfileActionResolver(inventory, registrations, actionSnapshotSource{}, configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		request contractv2.RunProfileActionRequest
		code    string
	}{
		"unknown repository":  {contractv2.RunProfileActionRequest{RepositoryID: "repository_02", ActionID: "tidy"}, "REPOSITORY_NOT_FOUND"},
		"unknown action":      {contractv2.RunProfileActionRequest{RepositoryID: "repository_01", ActionID: "nope"}, "ACTION_NOT_FOUND"},
		"foreign worktree":    {contractv2.RunProfileActionRequest{RepositoryID: "repository_01", ActionID: "tidy", WorktreeID: "worktree_other"}, "WORKTREE_NOT_FOUND"},
		"foreign environment": {contractv2.RunProfileActionRequest{RepositoryID: "repository_01", ActionID: "probe", EnvironmentID: "environment_other", ServiceID: "web"}, "ENVIRONMENT_NOT_FOUND"},
		"unknown service":     {contractv2.RunProfileActionRequest{RepositoryID: "repository_01", ActionID: "probe", EnvironmentID: "environment_linked", ServiceID: "db"}, "SERVICE_NOT_SUPPORTED"},
	}
	for name, c := range cases {
		_, err := resolver.ResolveAction(context.Background(), c.request)
		var actionError *daemon.ActionError
		if !errors.As(err, &actionError) || actionError.Contract.Code != c.code {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Desired configuration drifting from the accepted revision fails closed.
	contents, err := os.ReadFile(configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configurationPath, []byte(strings.Replace(string(contents), "displayName: Tidy", "displayName: Tidier", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = resolver.ResolveAction(context.Background(), contractv2.RunProfileActionRequest{RepositoryID: "repository_01", ActionID: "tidy", WorktreeID: "worktree_linked"})
	var actionError *daemon.ActionError
	if !errors.As(err, &actionError) || actionError.Contract.Code != "CONFIGURATION_NOT_ACCEPTED" {
		t.Fatalf("drifted configuration: %v", err)
	}
	list, err := resolver.ListActions(context.Background())
	if err != nil || len(list.Actions) != 4 || list.Actions[2].DisplayName != "Tidy" {
		t.Fatalf("listing must keep serving the pinned accepted revision: %+v err=%v", list.Actions, err)
	}
}

func TestConfiguredProfileActionResolverCompilesServiceActionsFromCurrentLeases(t *testing.T) {
	inventory, registrations, configurationPath := actionTestRegistrations(t)
	snapshot := contractv2.StatusSnapshot{Environments: []contractv2.Environment{{
		ID: "environment_linked", PortLeases: []contractv2.PortLease{{ServiceID: "web", Purpose: "http", Host: "127.0.0.1", Port: 30555}},
	}}}
	resolver, err := newConfiguredProfileActionResolver(inventory, registrations, actionSnapshotSource{snapshot: snapshot}, configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	request := contractv2.RunProfileActionRequest{RepositoryID: "repository_01", ActionID: "probe", EnvironmentID: "environment_linked", ServiceID: "web"}
	resolution, err := resolver.ResolveAction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	command, err := resolver.CompileAction(context.Background(), resolution, "operation_02")
	if err != nil || len(command.Arguments) != 1 || command.Arguments[0] != "http://localhost:30555/health" {
		t.Fatalf("service command: %+v err=%v", command, err)
	}
	// A stopped environment has no lease; the action fails closed instead of guessing a port.
	stopped, err := newConfiguredProfileActionResolver(inventory, registrations, actionSnapshotSource{}, configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stopped.CompileAction(context.Background(), resolution, "operation_03"); err == nil {
		t.Fatal("service action compiled without a live lease")
	}
	unavailable, err := newConfiguredProfileActionResolver(inventory, registrations, actionSnapshotSource{err: errors.New("locked")}, configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = unavailable.CompileAction(context.Background(), resolution, "operation_04")
	var actionError *daemon.ActionError
	if !errors.As(err, &actionError) || actionError.Contract.Code != "STATUS_UNAVAILABLE" {
		t.Fatalf("snapshot failure: %v", err)
	}
}
