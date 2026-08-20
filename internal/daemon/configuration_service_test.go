package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
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
	pending, err := service.Validate(ctx, contractv1.ConfigurationValidationRequest{SchemaVersion: 1})
	if err != nil || pending.State != "pending" || pending.Candidate == nil {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	accepted, err := service.Accept(ctx, contractv1.ConfigurationAcceptanceRequest{
		SchemaVersion: 1, ExpectedRevision: 0, Digest: pending.Candidate.Digest,
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
