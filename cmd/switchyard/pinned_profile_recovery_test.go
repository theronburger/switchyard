package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	profilecontrol "github.com/theronburger/switchyard/internal/control/profile"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
	"github.com/theronburger/switchyard/internal/state"
)

const pinnedRecoveryDocument = `schemaVersion: 1
machine:
  ports: {first: 30000, last: 49999}
  execution: {inheritedEnvironment: [], shellDefault: deny}
repositories:
  sample:
    enabled: true
    displayName: Sample
    root: ROOT
    git: {remote: origin, defaultBase: origin/main, managedWorktreesRoot: WORKTREES}
    values: {}
    toolchains: {}
    caches: {}
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
SERVICES
    infrastructure: {}
    artifacts: {}
    actions: {}
    cleanup: {}
`

const pinnedRecoveryWorker = `      worker:
        displayName: Worker
        kind: worker
        command: {executable: /usr/bin/true, arguments: [], workingDirectory: ., environment: {}, timeout: 30s}
`

func acceptRecoveryConfiguration(t *testing.T, store *state.Store, revision int64, root string, services string) configuration.Loaded {
	t.Helper()
	managedWorktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	document := strings.NewReplacer("ROOT", root, "WORKTREES", managedWorktreesRoot, "SERVICES\n", services).Replace(pinnedRecoveryDocument)
	loaded, err := configuration.Parse([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageConfiguration(context.Background(), revision, "compiler-v1", loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptConfiguration(context.Background(), revision, loaded.Digest); err != nil {
		t.Fatal(err)
	}
	return loaded
}

func recoveryRegistration(root string, profileDigest string, profile configuration.Repository) profilecontrol.Registration {
	runtimeRoot := filepath.Join(root, "runtime")
	return profilecontrol.Registration{
		EnvironmentID: "environment_01", RepositoryID: "repository_01", WorktreeID: "worktree_01",
		ProfileKey: "sample", ProfileDigest: profileDigest, RepositoryRoot: root, WorktreeRoot: root,
		RuntimeRoot: runtimeRoot, CacheRoot: filepath.Join(runtimeRoot, "caches"),
		HomeDirectory: filepath.Join(runtimeRoot, "home"), HostHomeDirectory: root, TemporaryDirectory: filepath.Join(runtimeRoot, "tmp"),
		ExecutablePath: "/usr/bin:/bin", DaemonInstanceID: "daemon_01", Values: map[string]string{}, Profile: profile,
	}
}

// publishPinnedRunningEnvironment persists a running environment whose worker
// service was started from the given repository-profile digest.
func publishPinnedRunningEnvironment(t *testing.T, store *state.Store, journal *state.EnvironmentJournal, profileDigest string) {
	t.Helper()
	ctx := context.Background()
	fingerprint, err := state.FingerprintRequest(map[string]string{"operationId": "operation_01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateOperation(ctx, state.NewOperation{
		ID: "operation_01", RunID: "run_01", RequestID: "request_01", IdempotencyKey: "key_01", RequestFingerprint: fingerprint,
		Kind: string(environmentcontrol.OperationStart), EnvironmentID: "environment_01",
	}); err != nil {
		t.Fatal(err)
	}
	record := environmentcontrol.OperationRecord{
		ID: "operation_01", EnvironmentID: "environment_01", RunID: "run_01",
		Kind: environmentcontrol.OperationStart, State: domain.OperationPending,
		EnvironmentState: domain.EnvironmentUnknown, Phase: environmentcontrol.PhasePending,
		Rollback: []environmentcontrol.RollbackEntry{},
		Intent:   &environmentcontrol.PlanIntent{ProfileDigest: profileDigest, TargetID: "local", ServiceIDs: []string{"worker"}},
	}
	if err := journal.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State, record.EnvironmentState, record.Phase = domain.OperationRunning, domain.EnvironmentStarting, environmentcontrol.PhaseWaitingReadiness
	if err := journal.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State, record.EnvironmentState, record.Phase = domain.OperationSucceeded, domain.EnvironmentRunning, environmentcontrol.PhaseComplete
	result := environmentcontrol.EnvironmentResult{
		EnvironmentID: "environment_01", RunID: "run_01", ProfileDigest: profileDigest,
		State: domain.EnvironmentRunning, Ports: []portlease.Lease{}, Infrastructure: []containerhost.Goal{},
		Services: []environmentcontrol.ServiceResult{{
			ID: "worker", EnvironmentID: "environment_01", RunID: "run_01", Owned: true,
			OwnershipPath: filepath.Join(t.TempDir(), "ownership.json"),
			Process: processhost.Ownership{
				EnvironmentID: "environment_01", ServiceID: "worker", RunID: "run_01",
				Members: []processhost.ProcessIdentity{{PID: 4242}}, StartedAt: time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC),
			},
		}},
		UpdatedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
	}
	if err := journal.Publish(ctx, record, result); err != nil {
		t.Fatal(err)
	}
}

func seedRecoverySnapshot(t *testing.T, store *state.Store) {
	t.Helper()
	snapshot := contractv2.StatusSnapshot{
		Daemon: contractv2.DaemonStatus{InstanceID: "daemon_01", Version: "test", State: "ready", StartedAt: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)},
		Repositories: []contractv2.Repository{{
			ID: "repository_01", DisplayName: "Sample", RootPath: "/tmp/sample", ProfileKey: "sample",
			Worktrees: []contractv2.Worktree{{ID: "worktree_01", Path: "/tmp/sample", HeadRevision: "abc"}},
		}},
		Environments: []contractv2.Environment{}, Operations: []contractv2.Operation{}, Alerts: []contractv2.Alert{},
	}
	if _, err := store.CommitSnapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
}

// TestPinnedProfileRecoverySurvivesLaterAcceptanceAndRestart proves that a
// running environment pinned to an older accepted revision is recovered from
// the exact retained payload after the head drops the service it runs, and
// that the public projection keeps rendering that service.
func TestPinnedProfileRecoverySurvivesLaterAcceptanceAndRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "state.sqlite")
	store, err := state.Open(ctx, state.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	seedRecoverySnapshot(t, store)
	withWorker := acceptRecoveryConfiguration(t, store, 0, root, pinnedRecoveryWorker)
	pinnedDigest := withWorker.RepositoryDigests["sample"]
	pinnedMetadata := configuredEnvironment{
		EnvironmentID: "environment_01", RepositoryID: "repository_01",
		Worktree:   contractv2.Worktree{ID: "worktree_01", Path: root},
		ProfileKey: "sample", ProfileDigest: pinnedDigest, Profile: withWorker.Document.Repositories["sample"],
	}
	journal, err := state.NewEnvironmentJournal(store, configuredEnvironmentProjector([]configuredEnvironment{pinnedMetadata}))
	if err != nil {
		t.Fatal(err)
	}
	publishPinnedRunningEnvironment(t, store, journal, pinnedDigest)

	// The owner accepts a head revision that no longer declares the worker.
	head := acceptRecoveryConfiguration(t, store, 1, root, "")
	headDigest := head.RepositoryDigests["sample"]
	if headDigest == pinnedDigest {
		t.Fatal("head and pinned digests must differ")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart: the registry is compiled from the head only.
	reopened, err := state.Open(ctx, state.Config{Path: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	headProfile := head.Document.Repositories["sample"]
	headMetadata := pinnedMetadata
	headMetadata.ProfileDigest, headMetadata.Profile = headDigest, headProfile
	projection := newConfiguredProjectionIndex([]configuredEnvironment{headMetadata})
	reopenedJournal, err := state.NewEnvironmentJournal(reopened, projection.projector())
	if err != nil {
		t.Fatal(err)
	}
	current, found, err := reopenedJournal.Current(ctx, "environment_01")
	if err != nil || !found || current.ProfileDigest != pinnedDigest {
		t.Fatalf("persisted result: %+v found=%v err=%v", current, found, err)
	}
	// Without the pin the head profile cannot even project the running worker.
	if _, err := projection.projector()(nil, current); err == nil {
		t.Fatal("head-only projection unexpectedly knew the pinned worker service")
	}

	registrations := []profilecontrol.Registration{recoveryRegistration(root, headDigest, headProfile)}
	pinnedRegistrations, pinnedEnvironments, err := recoverPinnedProfiles(ctx, reopened, reopenedJournal, registrations, []configuredEnvironment{headMetadata})
	if err != nil {
		t.Fatalf("recover pinned profiles: %v", err)
	}
	if len(pinnedRegistrations) != 1 || pinnedRegistrations[0].ProfileDigest != pinnedDigest ||
		pinnedRegistrations[0].Profile.Services["worker"].DisplayName != "Worker" ||
		len(pinnedEnvironments) != 1 || pinnedEnvironments[0].ProfileDigest != pinnedDigest {
		t.Fatalf("pinned registrations: %+v environments: %+v", pinnedRegistrations, pinnedEnvironments)
	}
	projection.addPinned(pinnedEnvironments)
	registry, err := profilecontrol.NewRegistry(registrations, pinnedRegistrations...)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := registry.LookupPinned("environment_01", pinnedDigest); err != nil || resolved.Profile.Services["worker"].Kind != "worker" {
		t.Fatalf("pinned registry lookup: %v", err)
	}
	projected, err := projection.projector()(nil, current)
	if err != nil || len(projected.Services) != 1 || projected.Services[0].ID != "worker" || projected.Services[0].DisplayName != "Worker" {
		t.Fatalf("pinned projection: %+v err=%v", projected, err)
	}
	// A new start still compiles only from the head.
	if _, err := registry.LookupPinned("environment_01", headDigest); err != nil {
		t.Fatalf("head lookup: %v", err)
	}
}

// TestPinnedProfileRecoveryFailsClosedWhenRevisionIsMissing proves the daemon
// refuses to substitute the head for a pinned payload that is not retained.
func TestPinnedProfileRecoveryFailsClosedWhenRevisionIsMissing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := state.Open(ctx, state.Config{Path: filepath.Join(root, "state.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedRecoverySnapshot(t, store)
	head := acceptRecoveryConfiguration(t, store, 0, root, pinnedRecoveryWorker)
	headProfile := head.Document.Repositories["sample"]
	metadata := configuredEnvironment{
		EnvironmentID: "environment_01", RepositoryID: "repository_01",
		Worktree:   contractv2.Worktree{ID: "worktree_01", Path: root},
		ProfileKey: "sample", ProfileDigest: head.RepositoryDigests["sample"], Profile: headProfile,
	}
	// The run is pinned to a digest whose accepted payload was never retained
	// in this database; seeding uses a permissive projector because the real
	// projector would already refuse to publish it.
	permissive := func(_ *contractv2.Environment, result environmentcontrol.EnvironmentResult) (contractv2.Environment, error) {
		return contractv2.Environment{
			ID: result.EnvironmentID, RepositoryID: "repository_01", WorktreeID: "worktree_01", DisplayName: "pinned",
			DesiredState: "running", ObservedState: "running", Health: "unknown",
			Services: []contractv2.Service{}, PortLeases: []contractv2.PortLease{}, InfrastructureLeases: []contractv2.InfrastructureLease{},
			URLs: map[string]string{}, AttentionAlertIDs: []string{},
		}, nil
	}
	journal, err := state.NewEnvironmentJournal(store, permissive)
	if err != nil {
		t.Fatal(err)
	}
	publishPinnedRunningEnvironment(t, store, journal, "sha256:"+strings.Repeat("d", 64))
	registrations := []profilecontrol.Registration{recoveryRegistration(root, head.RepositoryDigests["sample"], headProfile)}
	if _, _, err := recoverPinnedProfiles(ctx, store, journal, registrations, []configuredEnvironment{metadata}); err == nil ||
		!strings.Contains(err.Error(), "no longer retained") {
		t.Fatalf("missing pinned revision must fail closed: %v", err)
	}
}

// publishStoppedEnvironment persists a stopped environment result pinned to
// profileDigest: a start that failed and rolled back, leaving a durable but
// lifeless record behind.
func publishStoppedEnvironment(t *testing.T, store *state.Store, journal *state.EnvironmentJournal, environmentID string, profileDigest string) {
	t.Helper()
	ctx := context.Background()
	operationID := "operation_" + environmentID
	fingerprint, err := state.FingerprintRequest(map[string]string{"operationId": operationID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateOperation(ctx, state.NewOperation{
		ID: operationID, RunID: "run_" + environmentID, RequestID: "request_" + environmentID, IdempotencyKey: "key_" + environmentID,
		RequestFingerprint: fingerprint, Kind: string(environmentcontrol.OperationStart), EnvironmentID: environmentID,
	}); err != nil {
		t.Fatal(err)
	}
	record := environmentcontrol.OperationRecord{
		ID: operationID, EnvironmentID: environmentID, RunID: "run_" + environmentID,
		Kind: environmentcontrol.OperationStart, State: domain.OperationPending,
		EnvironmentState: domain.EnvironmentUnknown, Phase: environmentcontrol.PhasePending,
		Rollback: []environmentcontrol.RollbackEntry{},
		Intent:   &environmentcontrol.PlanIntent{ProfileDigest: profileDigest, TargetID: "local", ServiceIDs: []string{"worker"}},
	}
	if err := journal.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State, record.EnvironmentState, record.Phase = domain.OperationRunning, domain.EnvironmentStarting, environmentcontrol.PhaseLaunchingServices
	if err := journal.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.EnvironmentState, record.Phase = domain.EnvironmentStopping, environmentcontrol.PhaseRollingBack
	if err := journal.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State, record.EnvironmentState, record.Phase = domain.OperationFailed, domain.EnvironmentStopped, environmentcontrol.PhaseComplete
	record.Failure = "worker exited before readiness"
	result := environmentcontrol.EnvironmentResult{
		EnvironmentID: environmentID, RunID: "run_" + environmentID, ProfileDigest: profileDigest,
		State: domain.EnvironmentStopped, Ports: []portlease.Lease{}, Infrastructure: []containerhost.Goal{},
		Services: []environmentcontrol.ServiceResult{}, UpdatedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
	}
	if err := journal.Publish(ctx, record, result); err != nil {
		t.Fatal(err)
	}
}

// TestPinnedProfileRecoveryIgnoresStoppedResultsOfUnregisteredEnvironments
// proves that a stopped environment whose worktree or repository is no longer
// configured does not keep the daemon from booting: only live resources
// (running results and incomplete operations) fail closed.
func TestPinnedProfileRecoveryIgnoresStoppedResultsOfUnregisteredEnvironments(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := state.Open(ctx, state.Config{Path: filepath.Join(root, "state.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedRecoverySnapshot(t, store)
	head := acceptRecoveryConfiguration(t, store, 0, root, pinnedRecoveryWorker)
	headProfile := head.Document.Repositories["sample"]
	headDigest := head.RepositoryDigests["sample"]
	permissive := func(_ *contractv2.Environment, result environmentcontrol.EnvironmentResult) (contractv2.Environment, error) {
		return contractv2.Environment{
			ID: result.EnvironmentID, RepositoryID: "repository_01", WorktreeID: "worktree_01", DisplayName: "env",
			DesiredState: "stopped", ObservedState: "stopped", Health: "unknown",
			Services: []contractv2.Service{}, PortLeases: []contractv2.PortLease{}, InfrastructureLeases: []contractv2.InfrastructureLease{},
			URLs: map[string]string{}, AttentionAlertIDs: []string{},
		}, nil
	}
	journal, err := state.NewEnvironmentJournal(store, permissive)
	if err != nil {
		t.Fatal(err)
	}
	// environment_gone ran once from the head and stopped; its worktree was
	// archived afterwards, so the restarted daemon has no registration for it.
	publishStoppedEnvironment(t, store, journal, "environment_gone", headDigest)
	// environment_old stopped while pinned to a digest that is no longer
	// retained; a stopped result must not require that payload either.
	publishStoppedEnvironment(t, store, journal, "environment_old", "sha256:"+strings.Repeat("e", 64))

	registrations := []profilecontrol.Registration{recoveryRegistration(root, headDigest, headProfile)}
	metadata := configuredEnvironment{
		EnvironmentID: "environment_01", RepositoryID: "repository_01",
		Worktree:   contractv2.Worktree{ID: "worktree_01", Path: root},
		ProfileKey: "sample", ProfileDigest: headDigest, Profile: headProfile,
	}
	pinnedRegistrations, pinnedEnvironments, err := recoverPinnedProfiles(ctx, store, journal, registrations, []configuredEnvironment{metadata})
	if err != nil {
		t.Fatalf("stopped results of unregistered environments must not block boot: %v", err)
	}
	if len(pinnedRegistrations) != 0 || len(pinnedEnvironments) != 0 {
		t.Fatalf("unexpected pinned recovery: %+v %+v", pinnedRegistrations, pinnedEnvironments)
	}

	// With no accepted profile at all, stopped history must not block boot.
	if err := requireNoLiveEnvironments(ctx, journal); err != nil {
		t.Fatalf("stopped results must not require a profile: %v", err)
	}

	// A running result for an unregistered environment still fails closed:
	// the daemon never boots blind to processes it may own.
	running, err := state.NewEnvironmentJournal(store, permissive)
	if err != nil {
		t.Fatal(err)
	}
	publishPinnedRunningEnvironment(t, store, running, headDigest)
	if _, _, err := recoverPinnedProfiles(ctx, store, running, registrations, nil); err == nil ||
		!strings.Contains(err.Error(), "no longer belongs") {
		t.Fatalf("running result of an unregistered environment must fail closed: %v", err)
	}
	if err := requireNoLiveEnvironments(ctx, running); err == nil {
		t.Fatal("a running result must require an accepted profile")
	}
}
