package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	controlconfig "github.com/theronburger/switchyard/internal/control/config"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

func TestMarketplaceActionResolverBuildsRelativeAndLocalPreferredPorts(t *testing.T) {
	worktree := contractv1.Worktree{ID: "worktree_01", Path: "/tmp/marketplace", Branch: "feature/test"}
	registered := marketplaceEnvironment{
		EnvironmentID: "environment_01", RepositoryID: "repository_01", Worktree: worktree,
		PortDefaults: map[string]int{
			"DEED_ORGANIZER_PORT":         7005,
			"DEED_NONPROFIT_SERVICE_PORT": 4019,
		},
	}
	resolver := newMarketplaceActionResolver([]marketplaceEnvironment{registered}, marketplaceCatalogForTest())
	resolver.sourceReader = staticMarketplaceSourceReader{source: environmentcontrol.SourceSnapshot{
		Revision:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ObservedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC),
	}}
	resolution, err := resolver.ResolveStart(context.Background(), contractv1.StartEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request", IdempotencyKey: "key",
		},
		WorktreeID: worktree.ID, ServiceIDs: []string{"organizer", "nonprofit-service"},
	})
	if err != nil {
		t.Fatal(err)
	}
	preferred := make(map[string]int)
	for _, reservation := range resolution.Ports {
		if len(reservation.PreferredPorts) != 1 {
			t.Fatalf("reservation has no preferred port: %+v", reservation)
		}
		preferred[reservation.Key.ServiceID+"/"+reservation.Key.Purpose] = reservation.PreferredPorts[0]
	}
	want := map[string]int{
		"nonprofit-service/http":           4019,
		"nonprofit-service/lambda":         5019,
		"nonprofit-service/elasticmq-rest": 9324,
		"nonprofit-service/elasticmq-ui":   9325,
		"organizer/http":                   7005,
	}
	if !reflect.DeepEqual(preferred, want) {
		t.Fatalf("preferred ports: got %+v want %+v", preferred, want)
	}
	if resolution.EnvironmentID != registered.EnvironmentID ||
		resolution.Intent.TargetID != "testing" ||
		!reflect.DeepEqual(resolution.Intent.ServiceIDs, []string{"nonprofit-service", "organizer"}) {
		t.Fatalf("resolution: %+v", resolution)
	}
	_, err = resolver.ResolveStart(context.Background(), contractv1.StartEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request-production", IdempotencyKey: "key-production",
		},
		WorktreeID: worktree.ID, TargetID: "production", ServiceIDs: []string{"organizer"},
	})
	var confirmationError *daemon.ActionError
	if !errors.As(err, &confirmationError) || confirmationError.Contract.Code != "TARGET_CONFIRMATION_REQUIRED" {
		t.Fatalf("unconfirmed production target error: %v", err)
	}
	production, err := resolver.ResolveStart(context.Background(), contractv1.StartEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request-production-confirmed", IdempotencyKey: "key-production-confirmed",
		},
		WorktreeID: worktree.ID, TargetID: "production", ConfirmedTargetID: "production",
		ServiceIDs: []string{"organizer"},
	})
	if err != nil || production.Intent.TargetID != "production" {
		t.Fatalf("production resolution: %+v err=%v", production, err)
	}
	_, err = resolver.ResolveStart(context.Background(), contractv1.StartEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request-production-again", IdempotencyKey: "key-production-again",
		},
		WorktreeID: worktree.ID, TargetID: "production", ServiceIDs: []string{"organizer"},
	})
	if !errors.As(err, &confirmationError) || confirmationError.Contract.Code != "TARGET_CONFIRMATION_REQUIRED" {
		t.Fatalf("confirmation was incorrectly reused: %v", err)
	}
	_, err = resolver.ResolveStart(context.Background(), contractv1.StartEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request-demo-mismatch", IdempotencyKey: "key-demo-mismatch",
		},
		WorktreeID: worktree.ID, TargetID: "demo", ConfirmedTargetID: "production",
		ServiceIDs: []string{"organizer"},
	})
	if !errors.As(err, &confirmationError) || confirmationError.Contract.Code != "TARGET_CONFIRMATION_MISMATCH" {
		t.Fatalf("mismatched target confirmation error: %v", err)
	}
	_, err = resolver.ResolveStart(context.Background(), contractv1.StartEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request-unknown", IdempotencyKey: "key-unknown",
		},
		WorktreeID: worktree.ID, TargetID: "unknown", ServiceIDs: []string{"organizer"},
	})
	var actionError *daemon.ActionError
	if !errors.As(err, &actionError) || actionError.Contract.Code != "TARGET_NOT_SUPPORTED" {
		t.Fatalf("unknown target error: %v", err)
	}
}

type staticMarketplaceSourceReader struct {
	source environmentcontrol.SourceSnapshot
	err    error
}

func (reader staticMarketplaceSourceReader) Read(
	context.Context,
	string,
) (environmentcontrol.SourceSnapshot, error) {
	return reader.source, reader.err
}

func TestMarketplacePreferredPortsStillAllocateDistinctEnvironments(t *testing.T) {
	allocator, err := portlease.NewAllocator(portlease.Config{
		FirstPort: 30000, LastPort: 30010,
		Probe: func(context.Context, string, int) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := allocator.Reserve(context.Background(), portlease.Key{
		EnvironmentID: "environment_01", ServiceID: "organizer", Purpose: "http",
	}, 7005)
	if err != nil {
		t.Fatal(err)
	}
	second, err := allocator.Reserve(context.Background(), portlease.Key{
		EnvironmentID: "environment_02", ServiceID: "organizer", Purpose: "http",
	}, 7005)
	if err != nil {
		t.Fatal(err)
	}
	if first.Port != 7005 || second.Port == first.Port {
		t.Fatalf("leases were not isolated: first=%+v second=%+v", first, second)
	}
}

func TestMarketplaceNodeInstallPreparationUsesConstantShellAndArgumentVersion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := t.TempDir()
	temporary := t.TempDir()
	nvmScript := filepath.Join(t.TempDir(), "nvm.sh")
	if err := os.WriteFile(nvmScript, []byte("nvm() { return 0; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := applicationPaths{directory: filepath.Join(t.TempDir(), "support")}
	preparation, err := marketplaceNodeInstallPreparation(
		paths, root, home, temporary, nvmScript, "24",
	)
	if err != nil {
		t.Fatal(err)
	}
	if preparation.Executable != "/bin/bash" || len(preparation.Arguments) != 5 ||
		preparation.Arguments[2] != "switchyard-node-install" || preparation.Arguments[3] != nvmScript ||
		preparation.Arguments[4] != "24" || strings.Contains(preparation.Arguments[1], nvmScript) ||
		strings.Contains(preparation.Arguments[1], "24") {
		t.Fatalf("node install argv was not constant/exact: %+v", preparation)
	}
	if _, err := marketplaceNodeInstallPreparation(
		paths, root, home, temporary, nvmScript, "24; touch /tmp/foreign",
	); !errors.Is(err, errMarketplaceNodeUnavailable) {
		t.Fatalf("hostile Node constraint was accepted: %v", err)
	}
}

func TestMarketplaceEnvironmentProjectorProducesContractValidState(t *testing.T) {
	now := time.Date(2026, 8, 14, 16, 0, 0, 0, time.UTC)
	runtime, err := marketplaceRuntimeCatalog(controlconfig.RuntimeSettings{})
	if err != nil {
		t.Fatal(err)
	}
	worktree := contractv1.Worktree{
		ID: "worktree_01", Path: "/tmp/marketplace", Branch: "feature/test", HeadRevision: "abc",
	}
	metadata := marketplaceEnvironment{
		EnvironmentID: "environment_01", RepositoryID: "repository_01", Worktree: worktree,
		Runtime: runtime,
	}
	projector := marketplaceEnvironmentProjector([]marketplaceEnvironment{metadata}, marketplaceCatalogForTest())
	lease := portlease.Lease{
		Key: portlease.Key{
			EnvironmentID: metadata.EnvironmentID, ServiceID: "organizer", Purpose: "http",
		},
		Host: "127.0.0.1", Port: 17005,
	}
	projected, err := projector(nil, environmentcontrol.EnvironmentResult{
		EnvironmentID: metadata.EnvironmentID, RunID: "run_01", TargetID: "testing", State: domain.EnvironmentRunning,
		Ports: []portlease.Lease{lease}, Infrastructure: []containerhost.Goal{},
		Services: []environmentcontrol.ServiceResult{{
			ID: "organizer", EnvironmentID: metadata.EnvironmentID, RunID: "run_01", Owned: true,
			OwnershipPath: "/private/secret/worktree/.switchyard/ownership.json",
			Process: processhost.Ownership{
				EnvironmentID: metadata.EnvironmentID, ServiceID: "organizer", RunID: "run_01",
				StartedAt: now, Members: []processhost.ProcessIdentity{{PID: 100}},
				StdoutPath: "/private/secret/worktree/stdout.log",
			},
			Health: environmentcontrol.HealthReport{Readiness: "ready", Health: "healthy"},
			Observation: environmentcontrol.ServiceObservation{
				State: "running", ProcessCount: 3, MemoryBytes: 8192, ObservedAt: now,
			},
		}},
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := contractv1.StatusSnapshot{
		SchemaVersion: 1, SnapshotRevision: 1, GeneratedAt: now,
		Daemon: contractv1.DaemonStatus{
			InstanceID: "daemon_01", Version: "test", State: "ready", StartedAt: now,
		},
		Repositories: []contractv1.Repository{{
			ID: metadata.RepositoryID, DisplayName: "marketplace", RootPath: worktree.Path,
			Adapter: "marketplace", Worktrees: []contractv1.Worktree{worktree}, Runtime: &runtime,
		}},
		Environments: []contractv1.Environment{projected},
		Operations:   []contractv1.Operation{}, Alerts: []contractv1.Alert{},
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if projected.TargetID != "testing" || projected.Health != "healthy" || projected.URLs["organizer"] != "http://localhost:17005" ||
		len(projected.Services) != 1 || projected.Services[0].Run == nil ||
		projected.Services[0].Run.ProcessCount != 3 || projected.Services[0].Run.MemoryBytes != 8192 ||
		projected.Resources.MemoryBytes != 8192 || projected.Services[0].Run.CPUPercent != 0 {
		t.Fatalf("projected environment: %+v", projected)
	}
	payload, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "/private/secret") || strings.Contains(string(payload), "ownership.json") {
		t.Fatalf("public environment leaked a private runtime path: %s", payload)
	}
}

func TestMarketplaceEnvironmentProjectorPreservesFailedOwnershipState(t *testing.T) {
	now := time.Date(2026, 8, 17, 19, 9, 49, 0, time.UTC)
	runtime, err := marketplaceRuntimeCatalog(controlconfig.RuntimeSettings{})
	if err != nil {
		t.Fatal(err)
	}
	worktree := contractv1.Worktree{
		ID: "worktree_01", Path: "/tmp/marketplace", Branch: "feature/test", HeadRevision: "abc",
	}
	metadata := marketplaceEnvironment{
		EnvironmentID: "environment_01", RepositoryID: "repository_01", Worktree: worktree,
		Runtime: runtime,
	}
	projector := marketplaceEnvironmentProjector([]marketplaceEnvironment{metadata}, marketplaceCatalogForTest())
	result := environmentcontrol.EnvironmentResult{
		EnvironmentID: metadata.EnvironmentID, RunID: "run_01", TargetID: "testing",
		State: domain.EnvironmentFailed,
		Ports: []portlease.Lease{{
			Key:  portlease.Key{EnvironmentID: metadata.EnvironmentID, ServiceID: "organizer", Purpose: "http"},
			Host: "127.0.0.1", Port: 17005,
		}},
		Infrastructure: []containerhost.Goal{},
		Services: []environmentcontrol.ServiceResult{{
			ID: "organizer", EnvironmentID: metadata.EnvironmentID, RunID: "run_01", Owned: true,
			OwnershipPath: "/private/runtime/ownership.json",
			Process: processhost.Ownership{
				EnvironmentID: metadata.EnvironmentID, ServiceID: "organizer", RunID: "run_01",
				StartedAt: now, Members: []processhost.ProcessIdentity{},
			},
			Health: environmentcontrol.HealthReport{Readiness: "ready", Health: "degraded"},
			Observation: environmentcontrol.ServiceObservation{
				State: "unverifiable", Code: environmentcontrol.ServiceObservationOwnershipUnverified,
				ObservedAt: now,
			},
		}},
		UpdatedAt: now,
	}

	failed, err := projector(nil, result)
	if err != nil {
		t.Fatal(err)
	}
	if failed.DesiredState != "stopped" || failed.ObservedState != "failed" || failed.Health != "degraded" ||
		len(failed.Services) != 1 || failed.Services[0].DesiredState != "stopped" ||
		failed.Services[0].ObservedState != "unverifiable" ||
		failed.Services[0].ObservationCode != environmentcontrol.ServiceObservationOwnershipUnverified {
		t.Fatalf("failed projection: %+v", failed)
	}

	result.State = domain.EnvironmentRunning
	running, err := projector(nil, result)
	if err != nil {
		t.Fatal(err)
	}
	if running.DesiredState != "running" || running.ObservedState != "running" || running.Health != "degraded" {
		t.Fatalf("running degraded projection: %+v", running)
	}
}

func TestMarketplaceEnvironmentProjectorDoesNotPublishConfigurationOnlyHTTPPort(t *testing.T) {
	runtime, err := marketplaceRuntimeCatalog(controlconfig.RuntimeSettings{})
	if err != nil {
		t.Fatal(err)
	}
	metadata := marketplaceEnvironment{
		EnvironmentID: "environment_01", RepositoryID: "repository_01",
		Worktree: contractv1.Worktree{ID: "worktree_01", Path: "/tmp/marketplace", Branch: "feature/test"},
		Runtime:  runtime,
	}
	projected, err := marketplaceEnvironmentProjector(
		[]marketplaceEnvironment{metadata}, marketplaceCatalogForTest(),
	)(nil, environmentcontrol.EnvironmentResult{
		EnvironmentID: metadata.EnvironmentID, RunID: "run_01", TargetID: "testing",
		State: domain.EnvironmentStarting,
		Ports: []portlease.Lease{
			{Key: portlease.Key{EnvironmentID: metadata.EnvironmentID, ServiceID: "auth-service", Purpose: "http"}, Host: "127.0.0.1", Port: 4011},
			{Key: portlease.Key{EnvironmentID: metadata.EnvironmentID, ServiceID: "auth-service", Purpose: "lambda"}, Host: "127.0.0.1", Port: 5011},
		},
		UpdatedAt: time.Date(2026, 8, 16, 20, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, published := projected.URLs["auth-service"]; published {
		t.Fatalf("Auth configuration-only HTTP port was published as a URL: %+v", projected.URLs)
	}
}

func TestRuntimeResolutionUsesTrackedYarnAndCanonicalExecutable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	worktree := filepath.Join(t.TempDir(), "marketplace")
	if err := os.MkdirAll(filepath.Join(worktree, ".yarn", "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".nvmrc"), []byte("24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(worktree, ".yarnrc.yml"),
		[]byte("yarnPath: .yarn/releases/yarn-test.cjs\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	yarnPath := filepath.Join(worktree, ".yarn", "releases", "yarn-test.cjs")
	if err := os.WriteFile(yarnPath, []byte("module.exports = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nodePath := filepath.Join(home, ".nvm", "versions", "node", "v24.19.0", "bin", "node")
	if err := os.MkdirAll(filepath.Dir(nodePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodePath, []byte("node"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(t.TempDir(), "node-link")
	if err := os.Symlink(nodePath, linkPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv(nodeExecutableOverride, linkPath)
	resolvedNode, err := resolveNodeExecutable(worktree)
	if err != nil {
		t.Fatal(err)
	}
	resolvedYarn, err := resolveYarnCJS(worktree)
	if err != nil {
		t.Fatal(err)
	}
	canonicalNode, err := filepath.EvalSymlinks(nodePath)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedNode != canonicalNode || resolvedYarn != yarnPath {
		t.Fatalf("node=%q yarn=%q", resolvedNode, resolvedYarn)
	}
}

func marketplaceCatalogForTest() marketplaceadapter.Catalog {
	return marketplaceadapter.DefaultCatalog()
}
