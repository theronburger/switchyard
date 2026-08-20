package state

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/domain"
)

func acceptTestConfiguration(t *testing.T, store *Store, revision int64, displayName string) configuration.Loaded {
	t.Helper()
	document := strings.Replace(testConfigurationDocument, "displayName: Sample", "displayName: "+displayName, 1)
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

const testConfigurationDocument = `schemaVersion: 1
machine:
  ports: {first: 30000, last: 49999}
  execution: {inheritedEnvironment: [], shellDefault: deny}
secretProviders: {}
repositories:
  sample:
    enabled: true
    displayName: Sample
    root: /tmp/sample
    git: {remote: origin, defaultBase: origin/main, managedWorktreesRoot: /tmp/sample-worktrees}
    values: {}
    toolchains: {}
    caches: {}
    environmentSources: {}
    preparation: {}
    targets: {local: {}}
    defaultTarget: local
    services: {}
    infrastructure: {}
    artifacts: {}
    actions: {}
    cleanup: {}
`

func TestPinnedRepositoryProfileRecoversOlderAcceptedRevision(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	first := acceptTestConfiguration(t, store, 0, "First")
	second := acceptTestConfiguration(t, store, 1, "Second")
	if first.RepositoryDigests["sample"] == second.RepositoryDigests["sample"] {
		t.Fatal("test configurations must produce distinct repository digests")
	}
	pinned, err := store.PinnedRepositoryProfile(ctx, "sample", first.RepositoryDigests["sample"])
	if err != nil || pinned.DisplayName != "First" {
		t.Fatalf("pinned profile: %+v err=%v", pinned, err)
	}
	head, err := store.PinnedRepositoryProfile(ctx, "sample", second.RepositoryDigests["sample"])
	if err != nil || head.DisplayName != "Second" {
		t.Fatalf("head profile: %+v err=%v", head, err)
	}
	if _, err := store.PinnedRepositoryProfile(ctx, "sample", "sha256:unknown"); !errors.Is(err, ErrConfigurationRevisionMissing) {
		t.Fatalf("unknown digest: got %v", err)
	}
	if _, err := store.PinnedRepositoryProfile(ctx, "other", first.RepositoryDigests["sample"]); !errors.Is(err, ErrConfigurationRevisionMissing) {
		t.Fatalf("unknown key: got %v", err)
	}
}

func TestConfigurationRevisionRetentionKeepsHeadAndPinnedRevisions(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	seedEnvironmentJournalSnapshot(t, store)
	pinnedRevision := acceptTestConfiguration(t, store, 0, "Pinned")
	unreferenced := acceptTestConfiguration(t, store, 1, "Unreferenced")

	// A published running environment pins the first repository digest.
	createPublicEnvironmentOperation(t, store, "operation_pinned", "env_01", environmentcontrol.OperationStart)
	journal := newTestEnvironmentJournal(t, store, defaultProjector)
	record := pendingStartRecord("operation_pinned", "env_01")
	record.Intent = &environmentcontrol.PlanIntent{ProfileDigest: pinnedRevision.RepositoryDigests["sample"], ServiceIDs: []string{"web"}}
	if err := journal.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	record = runningRecord(record, environmentcontrol.PhaseWaitingReadiness)
	if err := journal.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State, record.EnvironmentState, record.Phase = domain.OperationSucceeded, domain.EnvironmentRunning, environmentcontrol.PhaseComplete
	result := successfulEnvironmentResult("env_01")
	result.ProfileDigest = pinnedRevision.RepositoryDigests["sample"]
	if err := journal.Publish(ctx, record, result); err != nil {
		t.Fatal(err)
	}

	for index := 0; index < retainedConfigurationRevisionLimit+4; index++ {
		acceptTestConfiguration(t, store, int64(index+2), fmt.Sprintf("Revision%02d", index))
	}
	var count int
	if err := store.database.QueryRowContext(ctx, "SELECT COUNT(*) FROM configuration_revisions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != retainedConfigurationRevisionLimit+1 {
		t.Fatalf("retained %d configuration revisions, want %d", count, retainedConfigurationRevisionLimit+1)
	}
	if pinned, err := store.PinnedRepositoryProfile(ctx, "sample", pinnedRevision.RepositoryDigests["sample"]); err != nil || pinned.DisplayName != "Pinned" {
		t.Fatalf("pinned revision was pruned: %+v err=%v", pinned, err)
	}
	if _, err := store.PinnedRepositoryProfile(ctx, "sample", unreferenced.RepositoryDigests["sample"]); !errors.Is(err, ErrConfigurationRevisionMissing) {
		t.Fatalf("unreferenced revision survived: %v", err)
	}
	head, err := store.ReadAcceptedConfiguration(ctx)
	if err != nil || head.Revision != int64(retainedConfigurationRevisionLimit+6) {
		t.Fatalf("head: %+v err=%v", head, err)
	}
}

func TestTerminalOperationRetentionIsBoundedAndPreservesReferencedWork(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	seedEnvironmentJournalSnapshot(t, store)

	// One published environment keeps its terminal start operation referenced.
	createPublicEnvironmentOperation(t, store, "operation_referenced", "env_01", environmentcontrol.OperationStart)
	journal := newTestEnvironmentJournal(t, store, defaultProjector)
	record := pendingStartRecord("operation_referenced", "env_01")
	if err := journal.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	record = runningRecord(record, environmentcontrol.PhaseWaitingReadiness)
	if err := journal.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.State, record.EnvironmentState, record.Phase = domain.OperationSucceeded, domain.EnvironmentRunning, environmentcontrol.PhaseComplete
	if err := journal.Publish(ctx, record, successfulEnvironmentResult("env_01")); err != nil {
		t.Fatal(err)
	}
	// One incomplete operation must never be pruned regardless of age.
	createPublicEnvironmentOperation(t, store, "operation_incomplete", "env_02", environmentcontrol.OperationStart)

	for index := 0; index < retainedTerminalOperationLimit+25; index++ {
		id := fmt.Sprintf("operation_%04d", index)
		fingerprint, err := FingerprintRequest(map[string]string{"operationId": id})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.CreateOperation(ctx, NewOperation{
			ID: id, RequestID: id, IdempotencyKey: id, RequestFingerprint: fingerprint, Kind: "workspace.prepare",
		}); err != nil {
			t.Fatal(err)
		}
		for _, next := range []string{"running", "succeeded"} {
			if _, err := store.TransitionOperation(ctx, id, next, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	fingerprint, err := FingerprintRequest(map[string]string{"operationId": "operation_trigger"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateOperation(ctx, NewOperation{
		ID: "operation_trigger", RequestID: "operation_trigger", IdempotencyKey: "operation_trigger",
		RequestFingerprint: fingerprint, Kind: "workspace.prepare",
	}); err != nil {
		t.Fatal(err)
	}

	operations, err := store.ListOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]contractv2.Operation, len(operations))
	for _, operation := range operations {
		byID[operation.ID] = operation
	}
	if _, kept := byID["operation_referenced"]; !kept {
		t.Fatal("operation referenced by the current environment result was pruned")
	}
	if _, kept := byID["operation_incomplete"]; !kept {
		t.Fatal("incomplete operation was pruned")
	}
	if _, kept := byID["operation_trigger"]; !kept {
		t.Fatal("new pending operation was pruned")
	}
	if _, pruned := byID["operation_0000"]; pruned {
		t.Fatal("oldest terminal operation survived beyond the retention bound")
	}
	if len(operations) > retainedTerminalOperationLimit+3 {
		t.Fatalf("ledger holds %d operations, want at most %d", len(operations), retainedTerminalOperationLimit+3)
	}
	snapshot, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Operations) != len(operations) {
		t.Fatalf("snapshot embeds %d operations, ledger has %d", len(snapshot.Operations), len(operations))
	}
	current, found, err := journal.Current(ctx, "env_01")
	if err != nil || !found || current.RunID != "run_env_01" {
		t.Fatalf("current environment result lost after pruning: found=%v err=%v", found, err)
	}
}
