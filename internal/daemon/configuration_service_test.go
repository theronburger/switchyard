package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

func TestConfigurationServiceStagesAndAcceptsOneRevision(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "configuration.yaml")
	if err := os.WriteFile(path, []byte(testServiceConfiguration), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(ctx, state.Config{Path: filepath.Join(directory, "state.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	restarted := make(chan struct{}, 1)
	service := ConfigurationService{Store: store, Path: path, CompilerVersion: "compiler-v1", Restart: func() { restarted <- struct{}{} }}
	pending, err := service.Validate(ctx, contractv2.ConfigurationValidationRequest{SchemaVersion: contractv2.SchemaVersion})
	if err != nil || pending.State != "pending" || pending.Candidate == nil {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	accepted, err := service.Accept(ctx, contractv2.ConfigurationAcceptanceRequest{
		SchemaVersion: contractv2.SchemaVersion, ExpectedRevision: 0, Digest: pending.Candidate.Digest,
	})
	if err != nil || accepted.State != "accepted" || accepted.AcceptedRevision != 1 {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	select {
	case <-restarted:
	case <-time.After(time.Second):
		t.Fatal("configuration acceptance did not restart")
	}
}

const testServiceConfiguration = `schemaVersion: 1
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

func newConfigurationServiceFixture(t *testing.T, initial string) (*ConfigurationService, string) {
	t.Helper()
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "Switchyard")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "configuration.yaml")
	if initial != "" {
		if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := state.Open(ctx, state.Config{Path: filepath.Join(directory, "state.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := &ConfigurationService{
		Store: store, Path: path, CompilerVersion: "compiler-v1", Restart: func() {},
		References: staticStatusSource{snapshot: validHTTPStatus()},
	}
	return service, path
}

func sampleEntry(key string, enabled bool) *contractv2.ConfigurationRepositoryEntry {
	return &contractv2.ConfigurationRepositoryEntry{
		Key: key, Enabled: enabled, DisplayName: "Sample " + key, Root: "/tmp/" + key,
		Remote: "origin", DefaultBase: "origin/main", ManagedWorktreesRoot: "/tmp/" + key + "-worktrees",
	}
}

func mutation(operation, key string, revision int64, source string, entry *contractv2.ConfigurationRepositoryEntry) contractv2.ConfigurationRepositoryMutationRequest {
	return contractv2.ConfigurationRepositoryMutationRequest{
		SchemaVersion: contractv2.SchemaVersion, ExpectedRevision: revision, ExpectedSourceDigest: source,
		Operation: operation, Key: key, Entry: entry,
	}
}

func acceptPending(t *testing.T, service *ConfigurationService, status contractv2.ConfigurationStatus) contractv2.ConfigurationStatus {
	t.Helper()
	if status.State != "pending" || status.Candidate == nil {
		t.Fatalf("expected pending candidate, got %+v", status)
	}
	accepted, err := service.Accept(context.Background(), contractv2.ConfigurationAcceptanceRequest{
		SchemaVersion: contractv2.SchemaVersion, ExpectedRevision: status.AcceptedRevision, Digest: status.Candidate.Digest,
	})
	if err != nil || accepted.State != "accepted" {
		t.Fatalf("accept: %+v %v", accepted, err)
	}
	return accepted
}

func TestConfigurationServiceAddsUpdatesDisablesAndRemovesThroughTheDaemon(t *testing.T) {
	ctx := context.Background()
	service, path := newConfigurationServiceFixture(t, "")

	// Add creates the desired file from nothing and stages a candidate.
	status, err := service.Status(ctx)
	if err != nil || status.State != "missing" || status.Desired == nil || status.Desired.Present {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	pending, err := service.MutateRepository(ctx, mutation("upsert", "alpha", 0, "", sampleEntry("alpha", true)))
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != "pending" || pending.Candidate == nil || pending.Desired == nil || !pending.Desired.Present ||
		len(pending.Desired.Repositories) != 1 || pending.Desired.Repositories[0].Key != "alpha" ||
		pending.Desired.SourceDigest != pending.Candidate.SourceDigest {
		t.Fatalf("add did not stage an exact candidate: %+v", pending)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("desired file is not private: %v %v", info, err)
	}
	if status, _ := service.Status(ctx); status.State != "missing" {
		t.Fatal("add must not change the accepted revision before acceptance")
	}
	accepted := acceptPending(t, service, pending)
	if accepted.AcceptedRevision != 1 {
		t.Fatalf("accepted=%+v", accepted)
	}

	// Add a second repository; update and disable the first.
	source := accepted.Desired.SourceDigest
	pending, err = service.MutateRepository(ctx, mutation("upsert", "beta", 1, source, sampleEntry("beta", true)))
	if err != nil || len(pending.Desired.Repositories) != 2 {
		t.Fatalf("second add: %+v %v", pending, err)
	}
	renamed := sampleEntry("alpha", false)
	renamed.DisplayName = "Alpha (paused)"
	renamed.DefaultBase = "origin/trunk"
	pending, err = service.MutateRepository(ctx, mutation("upsert", "alpha", 1, pending.Desired.SourceDigest, renamed))
	if err != nil {
		t.Fatal(err)
	}
	var alpha contractv2.ConfigurationRepositoryEntry
	for _, entry := range pending.Desired.Repositories {
		if entry.Key == "alpha" {
			alpha = entry
		}
	}
	if alpha.Enabled || alpha.DisplayName != "Alpha (paused)" || alpha.DefaultBase != "origin/trunk" || alpha.Root != "/tmp/alpha" {
		t.Fatalf("update did not land in the desired view: %+v", alpha)
	}

	// Removal is refused until the disabled revision is accepted.
	if _, err := service.MutateRepository(ctx, mutation("remove", "alpha", 1, pending.Desired.SourceDigest, nil)); !errors.Is(err, ErrConfigurationRepositoryEnabled) {
		t.Fatalf("removal before accepting the disable must fail, got %v", err)
	}
	accepted = acceptPending(t, service, pending)

	// Removal is refused while live resources still reference the repository.
	snapshot := validHTTPStatus()
	snapshot.Repositories = []contractv2.Repository{{
		ID: "repo_alpha", ProfileKey: "alpha", DisplayName: "Alpha", RootPath: "/tmp/alpha", Remote: "origin",
		Worktrees: []contractv2.Worktree{{ID: "wt_1", Path: "/tmp/alpha-worktrees/one", Workspace: &contractv2.WorkspaceStatus{Ownership: "managed"}}},
	}}
	service.References = staticStatusSource{snapshot: snapshot}
	if _, err := service.MutateRepository(ctx, mutation("remove", "alpha", 2, accepted.Desired.SourceDigest, nil)); !errors.Is(err, ErrConfigurationRepositoryReferenced) {
		t.Fatalf("removal with a managed worktree must fail, got %v", err)
	}
	snapshot.Repositories[0].Worktrees = nil
	snapshot.Environments = []contractv2.Environment{{ID: "env_1", RepositoryID: "repo_alpha", ObservedState: "running"}}
	service.References = staticStatusSource{snapshot: snapshot}
	if _, err := service.MutateRepository(ctx, mutation("remove", "alpha", 2, accepted.Desired.SourceDigest, nil)); !errors.Is(err, ErrConfigurationRepositoryReferenced) {
		t.Fatalf("removal with a running environment must fail, got %v", err)
	}
	service.References = nil
	if _, err := service.MutateRepository(ctx, mutation("remove", "alpha", 2, accepted.Desired.SourceDigest, nil)); !errors.Is(err, ErrConfigurationRepositoryReferenced) {
		t.Fatalf("removal without a reference source must fail closed, got %v", err)
	}
	snapshot.Environments[0].ObservedState = "stopped"
	service.References = staticStatusSource{snapshot: snapshot}
	pending, err = service.MutateRepository(ctx, mutation("remove", "alpha", 2, accepted.Desired.SourceDigest, nil))
	if err != nil || len(pending.Desired.Repositories) != 1 || pending.Desired.Repositories[0].Key != "beta" {
		t.Fatalf("remove: %+v %v", pending, err)
	}
	if _, stillKnown := pending.Candidate.RepositoryDigests["alpha"]; stillKnown {
		t.Fatal("removed repository still appears in the staged candidate")
	}
}

func TestConfigurationServiceMutationsFailClosed(t *testing.T) {
	ctx := context.Background()
	service, path := newConfigurationServiceFixture(t, testServiceConfiguration)
	original, _ := os.ReadFile(path)
	status, err := service.Status(ctx)
	if err != nil || status.Desired == nil || !status.Desired.Present || len(status.Desired.Repositories) != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	source := status.Desired.SourceDigest
	unchanged := func(label string) {
		t.Helper()
		current, _ := os.ReadFile(path)
		if string(current) != string(original) {
			t.Fatalf("%s modified the desired file", label)
		}
	}

	if _, err := service.MutateRepository(ctx, mutation("upsert", "other", 3, source, sampleEntry("other", true))); !errors.Is(err, state.ErrConfigurationRevisionConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	unchanged("stale revision")
	if _, err := service.MutateRepository(ctx, mutation("upsert", "other", 0, "", sampleEntry("other", true))); !errors.Is(err, ErrConfigurationDesiredChanged) {
		t.Fatalf("missing source digest on an existing file: %v", err)
	}
	stale := "sha256:" + strings.Repeat("0", 64)
	if _, err := service.MutateRepository(ctx, mutation("upsert", "other", 0, stale, sampleEntry("other", true))); !errors.Is(err, ErrConfigurationDesiredChanged) {
		t.Fatalf("stale source digest: %v", err)
	}
	unchanged("stale source digest")
	repointed := sampleEntry("sample", true)
	repointed.Root = "/tmp/elsewhere"
	if _, err := service.MutateRepository(ctx, mutation("upsert", "sample", 0, source, repointed)); !errors.Is(err, configuration.ErrRepositoryRootBound) {
		t.Fatalf("repoint: %v", err)
	}
	unchanged("repoint")
	if _, err := service.MutateRepository(ctx, mutation("remove", "absent", 0, source, nil)); !errors.Is(err, configuration.ErrRepositoryMissing) {
		t.Fatalf("remove absent: %v", err)
	}
	unchanged("remove absent")

	// A duplicate root fails the compiler before anything is written and the
	// owner sees the bounded reason.
	duplicateRoot := sampleEntry("other", true)
	duplicateRoot.Root = "/tmp/sample"
	var rejection ConfigurationRejectedError
	if _, err := service.MutateRepository(ctx, mutation("upsert", "other", 0, source, duplicateRoot)); !errors.As(err, &rejection) || !strings.Contains(rejection.Reason, "same root") {
		t.Fatalf("duplicate root: %v", err)
	}
	unchanged("duplicate root")

	// Removing the only repository leaves an empty catalog the compiler rejects.
	disabled := sampleEntry("sample", false)
	pending, err := service.MutateRepository(ctx, mutation("upsert", "sample", 0, source, disabled))
	if err != nil {
		t.Fatal(err)
	}
	acceptPending(t, service, pending)
	if _, err := service.MutateRepository(ctx, mutation("remove", "sample", 1, pending.Desired.SourceDigest, nil)); !errors.As(err, &rejection) || !strings.Contains(rejection.Reason, "at least one repository") {
		t.Fatalf("remove last: %v", err)
	}
	if len(configuration.ReadDesired(path).Entries()) != 1 {
		t.Fatal("refused removal modified the desired file")
	}
}

func TestConfigurationServiceRefusesUnsafeOrMalformedDesiredFiles(t *testing.T) {
	ctx := context.Background()
	t.Run("malformed YAML is reported and preserved", func(t *testing.T) {
		malformed := "schemaVersion: 1\nrepositories: [\n"
		service, path := newConfigurationServiceFixture(t, malformed)
		status, err := service.Status(ctx)
		if err != nil || status.Desired == nil || status.Desired.Problem == "" || status.Desired.SourceDigest == "" {
			t.Fatalf("status must surface the problem: %+v %v", status, err)
		}
		var rejection ConfigurationRejectedError
		if _, err := service.Validate(ctx, contractv2.ConfigurationValidationRequest{SchemaVersion: contractv2.SchemaVersion}); !errors.As(err, &rejection) {
			t.Fatalf("validate: %v", err)
		}
		if _, err := service.MutateRepository(ctx, mutation("upsert", "alpha", 0, status.Desired.SourceDigest, sampleEntry("alpha", true))); !errors.As(err, &rejection) {
			t.Fatalf("mutate: %v", err)
		}
		if current, _ := os.ReadFile(path); string(current) != malformed {
			t.Fatal("malformed desired file was modified")
		}
	})
	t.Run("symlinked desired file", func(t *testing.T) {
		service, path := newConfigurationServiceFixture(t, "")
		target := filepath.Join(t.TempDir(), "target.yaml")
		if err := os.WriteFile(target, []byte(testServiceConfiguration), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		status, err := service.Status(ctx)
		if err != nil || !status.Desired.Present || status.Desired.Problem == "" || status.Desired.SourceDigest != "" {
			t.Fatalf("status=%+v err=%v", status, err)
		}
		var rejection ConfigurationRejectedError
		if _, err := service.MutateRepository(ctx, mutation("upsert", "alpha", 0, "", sampleEntry("alpha", true))); !errors.As(err, &rejection) {
			t.Fatalf("symlink mutate: %v", err)
		}
		if current, _ := os.ReadFile(target); string(current) != testServiceConfiguration {
			t.Fatal("symlink target was modified")
		}
		if resolved, err := os.Lstat(path); err != nil || resolved.Mode()&os.ModeSymlink == 0 {
			t.Fatal("symlink was replaced")
		}
	})
	t.Run("hard-linked desired file", func(t *testing.T) {
		service, path := newConfigurationServiceFixture(t, "")
		target := filepath.Join(t.TempDir(), "target.yaml")
		if err := os.WriteFile(target, []byte(testServiceConfiguration), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(target, path); err != nil {
			t.Fatal(err)
		}
		var rejection ConfigurationRejectedError
		if _, err := service.MutateRepository(ctx, mutation("upsert", "alpha", 0, "", sampleEntry("alpha", true))); !errors.As(err, &rejection) {
			t.Fatalf("hard link mutate: %v", err)
		}
		if current, _ := os.ReadFile(target); string(current) != testServiceConfiguration {
			t.Fatal("hard link target was modified")
		}
	})
	t.Run("world-readable desired file", func(t *testing.T) {
		service, path := newConfigurationServiceFixture(t, testServiceConfiguration)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		var rejection ConfigurationRejectedError
		if _, err := service.MutateRepository(ctx, mutation("upsert", "alpha", 0, "", sampleEntry("alpha", true))); !errors.As(err, &rejection) {
			t.Fatalf("mode mutate: %v", err)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
			t.Fatal("refused write changed the file mode")
		}
	})
}
