package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
)

func TestConfigurationAcceptanceIsRevisionCheckedAndDurable(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 17, 0, 0, 0, time.UTC)
	databasePath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := Open(ctx, Config{Path: databasePath, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	loaded := testLoadedConfiguration(t)
	staged, err := store.StageConfiguration(ctx, 0, "compiler-v1", loaded)
	if err != nil {
		t.Fatal(err)
	}
	if staged.Digest != loaded.Digest || staged.StagedAt != now {
		t.Fatalf("staged configuration: %+v", staged)
	}
	if _, err := store.AcceptConfiguration(ctx, 1, loaded.Digest); !errors.Is(err, ErrConfigurationRevisionConflict) {
		t.Fatalf("accept conflict: got %v", err)
	}
	accepted, err := store.AcceptConfiguration(ctx, 0, loaded.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Revision != 1 || accepted.Digest != loaded.Digest || accepted.AcceptedAt != now {
		t.Fatalf("accepted configuration: %+v", accepted)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	persisted, err := reopened.ReadAcceptedConfiguration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != accepted.Revision || persisted.Digest != accepted.Digest ||
		string(persisted.CanonicalPayload) != string(loaded.CanonicalPayload) {
		t.Fatalf("persisted configuration: %+v", persisted)
	}
}

func TestConfigurationMustBeStagedBeforeAcceptance(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	if _, err := store.ReadAcceptedConfiguration(context.Background()); !errors.Is(err, ErrConfigurationNotAccepted) {
		t.Fatalf("empty head error: got %v", err)
	}
	if _, err := store.AcceptConfiguration(context.Background(), 0, "sha256:missing"); !errors.Is(err, ErrConfigurationCandidateMissing) {
		t.Fatalf("missing candidate error: got %v", err)
	}
}

func TestStagingUsesAcceptedHeadCAS(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	loaded := testLoadedConfiguration(t)
	if _, err := store.StageConfiguration(ctx, 0, "compiler-v1", loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptConfiguration(ctx, 0, loaded.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageConfiguration(ctx, 0, "compiler-v1", loaded); !errors.Is(err, ErrConfigurationRevisionConflict) {
		t.Fatalf("stale stage error: got %v", err)
	}
}

func TestStagingCannotReplaceAnExistingDigestPreview(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	loaded := testLoadedConfiguration(t)
	if _, err := store.StageConfiguration(ctx, 0, "compiler-v1", loaded); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StageConfiguration(ctx, 0, "compiler-v2", loaded); err == nil {
		t.Fatal("staged preview metadata was replaced for an existing digest")
	}
}

func testLoadedConfiguration(t *testing.T) configuration.Loaded {
	t.Helper()
	loaded, err := configuration.Parse([]byte(`schemaVersion: 1
machine:
  ports: {first: 30000, last: 49999}
  execution: {inheritedEnvironment: [], shellDefault: deny}
repositories:
  sample:
    enabled: true
    displayName: Sample
    root: /tmp/sample
    git: {remote: origin, defaultBase: origin/main, managedWorktreesRoot: /tmp/sample-worktrees}
    values: {}
    toolchains: {}
    caches: {}
    preparation: {}
    targets: {local: {}}
    defaultTarget: local
    services: {}
    infrastructure: {}
    artifacts: {}
    actions: {}
    cleanup: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}
