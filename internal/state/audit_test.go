package state

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/events"
)

func TestOperationAndConfigurationChangesEmitAuditEvents(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	if _, err := store.CommitSnapshot(ctx, auditSnapshot()); err != nil {
		t.Fatal(err)
	}
	operation, created, err := store.CreateOperation(ctx, NewOperation{
		ID: "operation_01", RequestID: "request_01", IdempotencyKey: "key_01", Kind: "workspace.prepare",
	})
	if err != nil || !created {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.TransitionOperation(ctx, operation.ID, "running", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOperation(ctx, operation.ID, "failed", &contractv2.ContractError{
		Code: "WORKSPACE_DIRTY", Message: "dirty", Diagnostic: "secret diagnostic text",
	}); err != nil {
		t.Fatal(err)
	}
	// An idempotent repeat must not add a second creation event.
	if _, created, err := store.CreateOperation(ctx, NewOperation{
		ID: "operation_dup", RequestID: "request_01", IdempotencyKey: "key_01", Kind: "workspace.prepare",
	}); err != nil || created {
		t.Fatalf("repeat create: created=%v err=%v", created, err)
	}
	page, err := store.ReadEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, event := range page.Events {
		kinds = append(kinds, event.Kind+":"+stateOf(event.Payload))
		if containsAny(string(event.Payload), "secret diagnostic", "dirty") {
			t.Fatalf("audit payload carried error text: %s", event.Payload)
		}
		if event.Revision <= 0 {
			t.Fatalf("audit event lacks the snapshot revision: %+v", event)
		}
	}
	want := fmt.Sprint([]string{
		events.KindOperationCreated + ":pending",
		events.KindOperationTransitioned + ":running",
		events.KindOperationTransitioned + ":failed",
	})
	if fmt.Sprint(kinds) != want {
		t.Fatalf("audit kinds: %v", kinds)
	}
	if !containsAny(string(page.Events[2].Payload), `"errorCode":"WORKSPACE_DIRTY"`) {
		t.Fatalf("terminal audit event lacks the stable error code: %s", page.Events[2].Payload)
	}
}

func auditSnapshot() contractv2.StatusSnapshot {
	snapshot := validSnapshot()
	snapshot.Repositories = []contractv2.Repository{{
		ID: "repo_01", DisplayName: "example", RootPath: "/tmp/repository", ProfileKey: "example",
		Worktrees: []contractv2.Worktree{{ID: "worktree_01", Path: "/tmp/worktree-1", HeadRevision: "abc"}},
	}}
	return snapshot
}

func stateOf(payload []byte) string {
	var decoded struct {
		State string `json:"state"`
	}
	_ = decodeStrict(payload, &decoded)
	return decoded.State
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && len(needle) <= len(value) && indexOf(value, needle) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(value, needle string) int {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return index
		}
	}
	return -1
}
