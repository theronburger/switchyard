package profile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theronburger/switchyard/internal/configuration"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

func TestProfilePlannerCompilesOnlyConfiguredBehavior(t *testing.T) {
	registration := profileRegistration(t)
	registry, err := NewRegistry([]Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	lease := portlease.Lease{
		Key:  portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: "web", Purpose: "http"},
		Host: "127.0.0.1", Port: 31001,
	}
	plan, err := NewPlanBuilder(registry).Build(environmentcontrol.PlanningRequest{
		EnvironmentID: registration.EnvironmentID, RunID: "run_01",
		Intent:        environmentcontrol.PlanIntent{Adapter: registration.ProfileDigest, TargetID: "local", ServiceIDs: []string{"web"}},
		AssignedPorts: []portlease.Lease{lease},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Services) != 1 || len(plan.Services[0].Process.Arguments) != 2 || plan.Services[0].Process.Arguments[1] != "31001" {
		t.Fatalf("plan: %+v", plan)
	}
	if len(plan.Projection.ArtifactIDs) != 1 || plan.Projection.ArtifactIDs[0] != "launcher" {
		t.Fatalf("artifact plan: %+v", plan.Projection)
	}
	if strings.HasPrefix(plan.Services[0].Process.Directory, registration.RuntimeRoot) {
		t.Fatalf("service working directory left the configured worktree: %s", plan.Services[0].Process.Directory)
	}
	for _, environment := range plan.Services[0].Process.Environment {
		if strings.HasPrefix(environment, "PORT=") && environment != "PORT=31001" {
			t.Fatalf("port environment: %s", environment)
		}
	}
}

func TestPrivateArtifactsRollbackRefusesModifiedFile(t *testing.T) {
	registration := profileRegistration(t)
	registry, err := NewRegistry([]Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	materializer := NewArtifactMaterializer(registry)
	change, err := materializer.Plan(context.Background(), registration.EnvironmentID, "run_01", environmentcontrol.ProjectionRequest{
		ID: artifactProjectionID, ArtifactIDs: []string{"launcher"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := materializer.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(registration.RuntimeRoot, "repositories", registration.ProfileKey, registration.WorktreeID,
		"environments", registration.EnvironmentID, "runs", "run_01", "artifacts", "launcher")
	if err := os.WriteFile(artifactPath, []byte("foreign change"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := materializer.Rollback(context.Background(), change); err == nil {
		t.Fatal("modified artifact was removed")
	}
	if contents, err := os.ReadFile(artifactPath); err != nil || string(contents) != "foreign change" {
		t.Fatalf("foreign artifact changed: %q %v", contents, err)
	}
}

func profileRegistration(t *testing.T) Registration {
	t.Helper()
	runtimeRoot := t.TempDir()
	worktree := t.TempDir()
	literal := func(value string) configuration.ValueRef { return configuration.ValueRef{Literal: &value} }
	worktreePath := "run.sh"
	profile := configuration.Repository{
		Enabled: true, DisplayName: "Sample", Root: worktree,
		Git:           configuration.Git{Remote: "origin", DefaultBase: "origin/main", ManagedWorktreesRoot: filepath.Join(t.TempDir(), "worktrees")},
		Targets:       map[string]configuration.Target{"local": {DisplayName: "Local", Risk: "local", Environment: map[string]configuration.ValueRef{}}},
		DefaultTarget: "local",
		Services: map[string]configuration.Service{
			"web": {
				DisplayName: "Web", Kind: "web", Ports: map[string]configuration.Port{"http": {}},
				Environment: map[string]configuration.ValueRef{"PORT": {Port: &configuration.PortReference{Purpose: "http"}}},
				Command: configuration.Command{
					Executable: "/usr/bin/true", WorkingDirectory: ".", Timeout: "30s",
					Arguments:   []configuration.ValueRef{{WorktreePath: &worktreePath}, {Port: &configuration.PortReference{Purpose: "http"}}},
					Environment: map[string]configuration.ValueRef{"MODE": literal("test")},
				},
				Artifacts: []string{"launcher"},
			},
		},
		Artifacts: map[string]configuration.Artifact{"launcher": {Content: "#!/bin/sh\n", Executable: true}},
	}
	return Registration{
		EnvironmentID: "environment_01", RepositoryID: "repository_01", WorktreeID: "worktree_01",
		ProfileKey: "sample", ProfileDigest: "sha256:" + strings.Repeat("a", 64),
		RepositoryRoot: worktree, WorktreeRoot: worktree, RuntimeRoot: runtimeRoot, CacheRoot: filepath.Join(runtimeRoot, "caches"),
		HomeDirectory: filepath.Join(runtimeRoot, "home"), TemporaryDirectory: filepath.Join(runtimeRoot, "tmp"),
		ExecutablePath: "/usr/bin:/bin", DaemonInstanceID: "daemon_01", Values: map[string]string{}, Profile: profile,
	}
}
