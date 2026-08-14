package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
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
		!reflect.DeepEqual(resolution.Intent.ServiceIDs, []string{"nonprofit-service", "organizer"}) {
		t.Fatalf("resolution: %+v", resolution)
	}
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

func TestMarketplaceEnvironmentProjectorProducesContractValidState(t *testing.T) {
	now := time.Date(2026, 8, 14, 16, 0, 0, 0, time.UTC)
	worktree := contractv1.Worktree{
		ID: "worktree_01", Path: "/tmp/marketplace", Branch: "feature/test", HeadRevision: "abc",
	}
	metadata := marketplaceEnvironment{
		EnvironmentID: "environment_01", RepositoryID: "repository_01", Worktree: worktree,
	}
	projector := marketplaceEnvironmentProjector([]marketplaceEnvironment{metadata}, marketplaceCatalogForTest())
	lease := portlease.Lease{
		Key: portlease.Key{
			EnvironmentID: metadata.EnvironmentID, ServiceID: "organizer", Purpose: "http",
		},
		Host: "127.0.0.1", Port: 17005,
	}
	projected, err := projector(nil, environmentcontrol.EnvironmentResult{
		EnvironmentID: metadata.EnvironmentID, RunID: "run_01", State: domain.EnvironmentRunning,
		Ports: []portlease.Lease{lease}, Infrastructure: []containerhost.Goal{},
		Services: []environmentcontrol.ServiceResult{{
			ID: "organizer", EnvironmentID: metadata.EnvironmentID, RunID: "run_01", Owned: true,
			Process: processhost.Ownership{
				EnvironmentID: metadata.EnvironmentID, ServiceID: "organizer", RunID: "run_01",
				StartedAt: now, Members: []processhost.ProcessIdentity{{PID: 100}},
			},
			Health: environmentcontrol.HealthReport{Readiness: "ready", Health: "healthy"},
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
			Adapter: "marketplace", Worktrees: []contractv1.Worktree{worktree},
		}},
		Environments: []contractv1.Environment{projected},
		Operations:   []contractv1.Operation{}, Alerts: []contractv1.Alert{},
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatal(err)
	}
	if projected.Health != "healthy" || projected.URLs["organizer"] != "http://127.0.0.1:17005" ||
		len(projected.Services) != 1 || projected.Services[0].Run == nil ||
		projected.Services[0].Run.ProcessCount != 1 {
		t.Fatalf("projected environment: %+v", projected)
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
