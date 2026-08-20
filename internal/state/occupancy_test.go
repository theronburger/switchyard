package state

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/events"
)

func occupancySnapshot() contractv2.StatusSnapshot {
	snapshot := validSnapshot()
	snapshot.Repositories = []contractv2.Repository{{
		ID: "repo_01", DisplayName: "example", RootPath: "/tmp/repository", ProfileKey: "example",
		Worktrees: []contractv2.Worktree{
			{ID: "worktree_01", Path: "/tmp/worktree-1", HeadRevision: "abc"},
			{ID: "worktree_02", Path: "/tmp/worktree-2", HeadRevision: "def"},
		},
	}}
	return snapshot
}

func heldLeases(t *testing.T, snapshot contractv2.StatusSnapshot, worktreeID string) []contractv2.OccupancyLease {
	t.Helper()
	for _, repository := range snapshot.Repositories {
		for _, worktree := range repository.Worktrees {
			if worktree.ID == worktreeID {
				return worktree.Occupancy
			}
		}
	}
	t.Fatalf("worktree %q not published", worktreeID)
	return nil
}

func TestOccupancyAcquireAndReleaseArePublishedAtomicallyWithAuditEvents(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	if _, err := store.CommitSnapshot(ctx, occupancySnapshot()); err != nil {
		t.Fatal(err)
	}
	before, _ := store.ReadSnapshot(ctx)

	lease, created, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
		ID: "occupancy_01", RequestID: "request_01", WorktreeID: "worktree_01",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	})
	if err != nil || !created {
		t.Fatalf("acquire: created=%v err=%v", created, err)
	}
	if lease.State != "held" || lease.ReleasedAt != nil || lease.AcquiredAt.IsZero() {
		t.Fatalf("lease: %+v", lease)
	}

	snapshot, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SnapshotRevision != before.SnapshotRevision+1 {
		t.Fatalf("acquire must advance the snapshot exactly once: %d -> %d", before.SnapshotRevision, snapshot.SnapshotRevision)
	}
	if published := heldLeases(t, snapshot, "worktree_01"); len(published) != 1 || published[0].ID != "occupancy_01" {
		t.Fatalf("published occupancy: %+v", published)
	}
	if other := heldLeases(t, snapshot, "worktree_02"); len(other) != 0 {
		t.Fatalf("unrelated worktree gained occupancy: %+v", other)
	}

	// Idempotent repeat returns the same lease without a second record.
	again, created, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
		ID: "occupancy_other", RequestID: "request_01", WorktreeID: "worktree_01",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	})
	if err != nil || created || again.ID != "occupancy_01" {
		t.Fatalf("repeat: created=%v id=%q err=%v", created, again.ID, err)
	}
	// A reused request ID with different content is refused.
	if _, _, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
		ID: "occupancy_other", RequestID: "request_01", WorktreeID: "worktree_02",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	}); !errors.Is(err, ErrOccupancyRequestReused) {
		t.Fatalf("reused request: %v", err)
	}

	released, err := store.ReleaseOccupancy(ctx, "worktree_01", "occupancy_01")
	if err != nil || released.State != "released" || released.ReleasedAt == nil {
		t.Fatalf("release: %+v %v", released, err)
	}
	snapshot, err = store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if published := heldLeases(t, snapshot, "worktree_01"); len(published) != 0 {
		t.Fatalf("released lease still published: %+v", published)
	}
	// Releasing again is idempotent.
	if again, err := store.ReleaseOccupancy(ctx, "worktree_01", "occupancy_01"); err != nil || again.State != "released" {
		t.Fatalf("repeat release: %+v %v", again, err)
	}
	// Release is bound to the worktree in the route.
	if _, err := store.ReleaseOccupancy(ctx, "worktree_02", "occupancy_01"); !errors.Is(err, ErrOccupancyLeaseNotFound) {
		t.Fatalf("cross-worktree release: %v", err)
	}
	if _, err := store.ReleaseOccupancy(ctx, "worktree_01", "missing"); !errors.Is(err, ErrOccupancyLeaseNotFound) {
		t.Fatalf("missing release: %v", err)
	}

	page, err := store.ReadEvents(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, event := range page.Events {
		kinds = append(kinds, event.Kind)
		if string(event.Payload) == "" || containsAny(string(event.Payload), "/tmp", "Agent task") {
			t.Fatalf("audit payload leaks display or path data: %s", event.Payload)
		}
	}
	if fmt.Sprint(kinds) != fmt.Sprint([]string{events.KindOccupancyAcquired, events.KindOccupancyReleased}) {
		t.Fatalf("audit kinds: %v", kinds)
	}
}

func TestOccupancyRefusesUnknownWorktreesAndEnforcesTheHeldLimit(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	if _, err := store.CommitSnapshot(ctx, occupancySnapshot()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
		ID: "occupancy_x", RequestID: "request_x", WorktreeID: "worktree_missing",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	}); !errors.Is(err, ErrOccupancyWorktreeUnknown) {
		t.Fatalf("unknown worktree: %v", err)
	}
	if _, _, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
		ID: "occupancy_bad", RequestID: "request_bad", WorktreeID: "worktree_01",
		HolderKind: "Agent Task", HolderLabel: "Agent task",
	}); err == nil {
		t.Fatal("invalid holder kind was accepted")
	}
	if _, _, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
		ID: "occupancy_bad", RequestID: "request_bad", WorktreeID: "worktree_01",
		HolderKind: "agent-task", HolderLabel: "/Users/someone/secret",
	}); err == nil {
		t.Fatal("path-like holder label was accepted")
	}
	for index := 0; index < contractv2.MaximumHeldOccupancyLeases; index++ {
		if _, _, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
			ID: fmt.Sprintf("occupancy_%02d", index), RequestID: fmt.Sprintf("request_%02d", index),
			WorktreeID: "worktree_01", HolderKind: "agent-task", HolderLabel: "Agent task",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
		ID: "occupancy_overflow", RequestID: "request_overflow", WorktreeID: "worktree_01",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	}); !errors.Is(err, ErrOccupancyLimit) {
		t.Fatalf("limit: %v", err)
	}
	// The other worktree is unaffected by the first worktree's limit.
	if _, _, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
		ID: "occupancy_other", RequestID: "request_other", WorktreeID: "worktree_02",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(heldLeases(t, snapshot, "worktree_01")) != contractv2.MaximumHeldOccupancyLeases {
		t.Fatal("held leases were not all published")
	}
}

func TestHeldOccupancySurvivesReopenAndReprojection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, path)
	if _, err := store.CommitSnapshot(ctx, occupancySnapshot()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
		ID: "occupancy_01", RequestID: "request_01", WorktreeID: "worktree_01",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	held, err := reopened.ListHeldOccupancy(ctx)
	if err != nil || len(held) != 1 || held[0].ID != "occupancy_01" {
		t.Fatalf("held after reopen: %+v %v", held, err)
	}
	// An inventory rebuild that drops worktree occupancy is repaired by
	// re-projecting the durable leases.
	rebuilt := occupancySnapshot()
	ProjectOccupancy(&rebuilt, held)
	if leases := heldLeases(t, rebuilt, "worktree_01"); len(leases) != 1 {
		t.Fatalf("re-projection lost the lease: %+v", leases)
	}
	forWorktree, err := reopened.HeldOccupancyForWorktree(ctx, "worktree_02")
	if err != nil || len(forWorktree) != 0 {
		t.Fatalf("worktree_02 held: %+v %v", forWorktree, err)
	}
}

func TestReleasedOccupancyHistoryIsBounded(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "state.sqlite"))
	store.now = func() time.Time { return time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC) }
	if _, err := store.CommitSnapshot(ctx, occupancySnapshot()); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < retainedReleasedLeaseLimit+5; index++ {
		id := fmt.Sprintf("occupancy_%04d", index)
		if _, _, err := store.AcquireOccupancy(ctx, NewOccupancyLease{
			ID: id, RequestID: "request_" + id, WorktreeID: "worktree_01",
			HolderKind: "agent-task", HolderLabel: "Agent task",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReleaseOccupancy(ctx, "worktree_01", id); err != nil {
			t.Fatal(err)
		}
	}
	var released int
	if err := store.database.QueryRowContext(ctx, "SELECT COUNT(*) FROM occupancy_leases WHERE state = 'released'").Scan(&released); err != nil {
		t.Fatal(err)
	}
	if released != retainedReleasedLeaseLimit {
		t.Fatalf("released history: got %d, want %d", released, retainedReleasedLeaseLimit)
	}
}

func TestMigration11AddsOccupancyToAnExisting010Database(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, path)
	if _, err := store.CommitSnapshot(ctx, occupancySnapshot()); err != nil {
		t.Fatal(err)
	}
	// Roll the ledger back to a database that predates occupancy leases.
	for _, statement := range []string{
		"DROP TABLE occupancy_leases",
		"DELETE FROM schema_migrations WHERE version = 11",
	} {
		if _, err := store.database.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	migrated := openTestStore(t, path)
	var version int
	if err := migrated.database.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil || version != 11 {
		t.Fatalf("schema version after reopen: %d %v", version, err)
	}
	if _, _, err := migrated.AcquireOccupancy(ctx, NewOccupancyLease{
		ID: "occupancy_01", RequestID: "request_01", WorktreeID: "worktree_01",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	}); err != nil {
		t.Fatalf("acquire after migration: %v", err)
	}
}

func TestMigrationRefusesADatabaseThatIsNewerThanOccupancy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store := openTestStore(t, path)
	if _, err := store.database.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES (99, '2026-08-21T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(ctx, Config{Path: path})
	if !errors.Is(err, ErrUnsupportedSchemaVersion) {
		t.Fatalf("newer database: %v", err)
	}
}
