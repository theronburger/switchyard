package profile

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/health"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

type neverReadyProber struct{}

func (neverReadyProber) Check(context.Context, health.ProbeSpec) (health.ProbeResult, error) {
	return health.ProbeResult{Success: false}, nil
}

func TestProfilePlannerCompilesOnlyConfiguredBehavior(t *testing.T) {
	registration := profileRegistration(t)
	target := registration.Profile.Targets["local"]
	target.Environment["HOME"] = configuration.ValueRef{HostHome: true}
	registration.Profile.Targets["local"] = target
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
		Intent:        environmentcontrol.PlanIntent{ProfileDigest: registration.ProfileDigest, TargetID: "local", ServiceIDs: []string{"web"}},
		AssignedPorts: []portlease.Lease{lease},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ServiceStages) != 1 || len(plan.ServiceStages[0]) != 1 || len(plan.ServiceStages[0][0].Process.Arguments) != 2 || plan.ServiceStages[0][0].Process.Arguments[1] != "31001" {
		t.Fatalf("plan: %+v", plan)
	}
	service := plan.ServiceStages[0][0]
	if len(plan.Projection.ArtifactIDs) != 1 || plan.Projection.ArtifactIDs[0] != "launcher" {
		t.Fatalf("artifact plan: %+v", plan.Projection)
	}
	if strings.HasPrefix(service.Process.Directory, registration.RuntimeRoot) {
		t.Fatalf("service working directory left the configured worktree: %s", service.Process.Directory)
	}
	foundHostHome := false
	for _, environment := range service.Process.Environment {
		if strings.HasPrefix(environment, "PORT=") && environment != "PORT=31001" {
			t.Fatalf("port environment: %s", environment)
		}
		if environment == "HOME="+registration.HostHomeDirectory {
			foundHostHome = true
		}
	}
	if !foundHostHome {
		t.Fatalf("explicit host home was not compiled: %v", service.Process.Environment)
	}
}

func TestProfilePlannerNeverReadsRepositoryDotenvFiles(t *testing.T) {
	registration := profileRegistration(t)
	if err := os.WriteFile(filepath.Join(registration.WorktreeRoot, ".env.development"), []byte("SENTINEL_CREDENTIAL=must-not-cross\nPORT=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := registration.Profile.Targets["local"]
	target.Environment["DEPLOYMENT_ENVIRONMENT"] = configuration.ValueRef{Literal: stringPointer("development")}
	registration.Profile.Targets["local"] = target
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
		Intent:        environmentcontrol.PlanIntent{ProfileDigest: registration.ProfileDigest, TargetID: "local", ServiceIDs: []string{"web"}},
		AssignedPorts: []portlease.Lease{lease},
	})
	if err != nil {
		t.Fatal(err)
	}
	environment := plan.ServiceStages[0][0].Process.Environment
	if slices.Contains(environment, "SENTINEL_CREDENTIAL=must-not-cross") {
		t.Fatalf("repository dotenv value crossed into the plan: %v", environment)
	}
	if !slices.Contains(environment, "DEPLOYMENT_ENVIRONMENT=development") || !slices.Contains(environment, "PORT=31001") {
		t.Fatalf("selector or leased port missing: %v", environment)
	}
}

func TestPublishedRoutesOverrideConfiguredEnvironmentValues(t *testing.T) {
	registration := profileRegistration(t)
	service := registration.Profile.Services["web"]
	service.Ports["http"] = configuration.Port{Publish: []configuration.PublishedURL{{Name: "WEB_URL", Scheme: "http", Host: "localhost", Path: "/"}}}
	// Every configured layer tries to shadow the Switchyard-owned route.
	service.Environment["WEB_URL"] = configuration.ValueRef{Literal: stringPointer("service-shadow")}
	service.Command.Environment["WEB_URL"] = configuration.ValueRef{Literal: stringPointer("command-shadow")}
	service.Prepare = []configuration.Command{{
		Executable: "/usr/bin/true", WorkingDirectory: ".", Timeout: "30s",
		Environment: map[string]configuration.ValueRef{"WEB_URL": {Literal: stringPointer("prepare-shadow")}},
	}}
	registration.Profile.Services["web"] = service
	target := registration.Profile.Targets["local"]
	target.Environment["WEB_URL"] = configuration.ValueRef{Literal: stringPointer("target-shadow")}
	registration.Profile.Targets["local"] = target
	registry, err := NewRegistry([]Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlanBuilder(registry).Build(environmentcontrol.PlanningRequest{
		EnvironmentID: registration.EnvironmentID, RunID: "run_01",
		Intent:        environmentcontrol.PlanIntent{ProfileDigest: registration.ProfileDigest, TargetID: "local", ServiceIDs: []string{"web"}},
		AssignedPorts: []portlease.Lease{{Key: portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: "web", Purpose: "http"}, Host: "127.0.0.1", Port: 31001}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "WEB_URL=http://localhost:31001/"
	if environment := plan.ServiceStages[0][0].Process.Environment; !slices.Contains(environment, want) {
		t.Fatalf("service environment lost route precedence: %v", environment)
	}
	if len(plan.Preparations) != 1 || !slices.Contains(plan.Preparations[0].Environment, want) {
		t.Fatalf("preparation environment lost route precedence: %+v", plan.Preparations)
	}
	for _, entry := range append(append([]string{}, plan.ServiceStages[0][0].Process.Environment...), plan.Preparations[0].Environment...) {
		if strings.HasSuffix(entry, "-shadow") {
			t.Fatalf("configured value shadowed a published route: %s", entry)
		}
	}
}

func TestInheritedEnvironmentSitsBeneathEveryOtherLayer(t *testing.T) {
	registration := profileRegistration(t)
	registration.InheritedEnvironment = map[string]string{"LANG": "C.UTF-8", "MODE": "inherited-shadow"}
	registry, err := NewRegistry([]Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlanBuilder(registry).Build(environmentcontrol.PlanningRequest{
		EnvironmentID: registration.EnvironmentID, RunID: "run_01",
		Intent:        environmentcontrol.PlanIntent{ProfileDigest: registration.ProfileDigest, TargetID: "local", ServiceIDs: []string{"web"}},
		AssignedPorts: []portlease.Lease{{Key: portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: "web", Purpose: "http"}, Host: "127.0.0.1", Port: 31001}},
	})
	if err != nil {
		t.Fatal(err)
	}
	environment := plan.ServiceStages[0][0].Process.Environment
	if !slices.Contains(environment, "LANG=C.UTF-8") || !slices.Contains(environment, "MODE=test") || slices.Contains(environment, "MODE=inherited-shadow") {
		t.Fatalf("inherited precedence: %v", environment)
	}
	// An inherited entry can never replace the explicit trusted base.
	registration.InheritedEnvironment = map[string]string{"HOME": "/hostile"}
	if _, err := NewRegistry([]Registration{registration}); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("trusted base override through inheritance: %v", err)
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

func TestProfilePlannerStagesDependenciesBehindReadinessBarriers(t *testing.T) {
	registration := profileRegistration(t)
	registration.Profile.Services["worker"] = configuration.Service{
		DisplayName: "Worker", Kind: "worker", Dependencies: []string{"web"},
		Ports: map[string]configuration.Port{}, Environment: map[string]configuration.ValueRef{},
		Command: configuration.Command{Executable: "/usr/bin/true", WorkingDirectory: ".", Environment: map[string]configuration.ValueRef{}, Timeout: "30s"},
	}
	registry, err := NewRegistry([]Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlanBuilder(registry).Build(environmentcontrol.PlanningRequest{
		EnvironmentID: registration.EnvironmentID, RunID: "run_01",
		Intent:        environmentcontrol.PlanIntent{ProfileDigest: registration.ProfileDigest, TargetID: "local", ServiceIDs: []string{"worker"}},
		AssignedPorts: []portlease.Lease{{Key: portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: "web", Purpose: "http"}, Host: "127.0.0.1", Port: 31001}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ServiceStages) != 2 || len(plan.ServiceStages[0]) != 1 || plan.ServiceStages[0][0].ID != "web" ||
		len(plan.ServiceStages[1]) != 1 || plan.ServiceStages[1][0].ID != "worker" {
		t.Fatalf("stages: %+v", plan.ServiceStages)
	}
}

func TestConfiguredInfrastructurePortsAreNotProbedAsFreeBeforeServiceLaunch(t *testing.T) {
	profile := configuration.Repository{Infrastructure: map[string]configuration.Infrastructure{
		"queue": {ContainerPorts: map[string]configuration.ContainerPort{
			"rest": {Service: "web", Purpose: "queue", ContainerPort: 9324},
		}},
	}}
	ports := configuredInfrastructurePorts(profile, "web", []string{"queue"})
	if _, found := ports["queue"]; !found || len(ports) != 1 {
		t.Fatalf("infrastructure ports: %+v", ports)
	}
}

func TestConfiguredReadinessTimeoutOverridesTheDefault(t *testing.T) {
	registration := profileRegistration(t)
	service := registration.Profile.Services["web"]
	service.ReadinessTimeout = "5ms"
	service.Readiness = []configuration.Probe{{Kind: "tcp", Port: "http"}}
	registration.Profile.Services["web"] = service
	registry, err := NewRegistry([]Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	checker, err := NewReadinessChecker(registry, neverReadyProber{})
	if err != nil {
		t.Fatal(err)
	}
	checker.maximumWait = time.Minute
	checker.interval = time.Millisecond
	target := environmentcontrol.ReadinessTarget{
		EnvironmentID: registration.EnvironmentID, RunID: "run_01",
		Service: environmentcontrol.ServiceResult{ID: "web", EnvironmentID: registration.EnvironmentID, RunID: "run_01"},
		Ports:   []portlease.Lease{{Key: portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: "web", Purpose: "http"}, Host: "127.0.0.1", Port: 31001}},
		Spec:    environmentcontrol.ReadinessSpec{ID: readinessID("web")},
	}
	err = checker.WaitReady(context.Background(), target)
	if !errors.Is(err, environmentcontrol.ErrReadinessTimedOut) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness error: %v", err)
	}
	// A caller cancellation is reported as cancellation, never as the
	// service's readiness timeout.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err = checker.WaitReady(cancelled, target)
	if !errors.Is(err, context.Canceled) || errors.Is(err, environmentcontrol.ErrReadinessTimedOut) {
		t.Fatalf("cancelled readiness error: %v", err)
	}
}

func TestPrivateArtifactSegmentsResolveLeasedValuesOutsideWorktree(t *testing.T) {
	registration := profileRegistration(t)
	registration.Profile.Artifacts["launcher"] = configuration.Artifact{
		Filename: "launcher.cjs",
		Segments: []configuration.ValueRef{
			{Literal: stringPointer("const url = \"")},
			{URL: &configuration.URLReference{Service: "web", Purpose: "http", Scheme: "http", Host: "localhost", Path: "/ready"}},
			{Literal: stringPointer("\";\n")},
		},
	}
	registry, err := NewRegistry([]Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	materializer := NewArtifactMaterializer(registry)
	change, err := materializer.Plan(context.Background(), registration.EnvironmentID, "run_01", environmentcontrol.ProjectionRequest{
		ID: artifactProjectionID, ArtifactIDs: []string{"launcher"},
	}, []portlease.Lease{{Key: portlease.Key{EnvironmentID: registration.EnvironmentID, ServiceID: "web", Purpose: "http"}, Host: "127.0.0.1", Port: 31001}})
	if err != nil {
		t.Fatal(err)
	}
	if err := materializer.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(registration.RuntimeRoot, "repositories", registration.ProfileKey, registration.WorktreeID,
		"environments", registration.EnvironmentID, "runs", "run_01", "artifacts", "launcher.cjs")
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "const url = \"http://localhost:31001/ready\";\n" {
		t.Fatalf("artifact: %q %v", contents, err)
	}
	if strings.HasPrefix(path, registration.WorktreeRoot) {
		t.Fatalf("artifact escaped private runtime: %s", path)
	}
}

func TestArtifactRollbackTokenNeverCarriesResolvedContent(t *testing.T) {
	registration := profileRegistration(t)
	registration.Values = map[string]string{"build-tag": "sentinel-worktree-metadata"}
	registration.Profile.Values = map[string]configuration.ValueSource{"build-tag": {Kind: "text-file", Root: "worktree", Path: "VERSION"}}
	registration.Profile.Artifacts["launcher"] = configuration.Artifact{
		Filename: "launcher.cjs",
		Segments: []configuration.ValueRef{{Literal: stringPointer("tag=")}, {Value: "build-tag"}},
	}
	registration.Profile.Artifacts["static"] = configuration.Artifact{Content: "static-sentinel-body\n"}
	registration.Profile.Artifacts["static-copy"] = configuration.Artifact{Content: "static-sentinel-body\n"}
	registry, err := NewRegistry([]Registration{registration})
	if err != nil {
		t.Fatal(err)
	}
	materializer := NewArtifactMaterializer(registry)
	change, err := materializer.Plan(context.Background(), registration.EnvironmentID, "run_01", environmentcontrol.ProjectionRequest{
		ID: artifactProjectionID, ArtifactIDs: []string{"launcher", "static", "static-copy"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(change.RollbackToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"sentinel-worktree-metadata", "tag=", "static-sentinel-body"} {
		if strings.Contains(string(decoded), forbidden) {
			t.Fatalf("rollback token persisted resolved content %q: %s", forbidden, decoded)
		}
	}
	if err := materializer.Apply(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	artifactDirectory := filepath.Join(registration.RuntimeRoot, "repositories", registration.ProfileKey, registration.WorktreeID,
		"environments", registration.EnvironmentID, "runs", "run_01", "artifacts")
	if contents, err := os.ReadFile(filepath.Join(artifactDirectory, "launcher.cjs")); err != nil || string(contents) != "tag=sentinel-worktree-metadata" {
		t.Fatalf("materialized segment artifact: %q %v", contents, err)
	}
	for _, filename := range []string{"static", "static-copy"} {
		if contents, err := os.ReadFile(filepath.Join(artifactDirectory, filename)); err != nil || string(contents) != "static-sentinel-body\n" {
			t.Fatalf("materialized duplicate-content artifact %s: %q %v", filename, contents, err)
		}
	}
	// Re-applying the same change is idempotent from the disk alone.
	if err := materializer.Apply(context.Background(), change); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	// Rollback works from the digest-only token, as it must after a restart.
	restarted := NewArtifactMaterializer(registry)
	if err := restarted.Rollback(context.Background(), change); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(artifactDirectory, "launcher.cjs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact survived rollback: %v", err)
	}
	// A digest-only token cannot be applied by a materializer that never
	// planned it: there is no content to write and nothing matching on disk.
	if err := restarted.Apply(context.Background(), change); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("apply without planned content: %v", err)
	}
}

func stringPointer(value string) *string { return &value }

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
		HostHomeDirectory: filepath.Join(runtimeRoot, "host-home"),
		ExecutablePath:    "/usr/bin:/bin", DaemonInstanceID: "daemon_01", Values: map[string]string{}, Profile: profile,
	}
}
