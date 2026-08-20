package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	cleanupcontrol "github.com/theronburger/switchyard/internal/control/cleanup"
)

func TestCleanupPlanIsRevisionCheckedExpiringAndSingleUse(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 21, 0, 0, 0, time.UTC)
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "state.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	plan, err := store.SaveCleanupPlan(ctx, cleanupcontrol.Plan{
		SchemaVersion: 1, ID: "plan_01", Scope: cleanupcontrol.Scope{Kind: "global"},
		Candidates: []cleanupcontrol.Candidate{{ID: "candidate_01", Path: "/private/path", Device: 1, Inode: 2}},
		Protected:  []cleanupcontrol.Protection{}, CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	if err != nil || plan.Revision != 1 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if _, err := store.ReadCleanupPlan(ctx, plan.ID, plan.Revision+1); !errors.Is(err, ErrCleanupPlanNotFound) {
		t.Fatalf("revision error: %v", err)
	}
	stored, err := store.ReadCleanupPlan(ctx, plan.ID, plan.Revision)
	if err != nil || stored.Candidates[0].Path != "/private/path" || stored.Candidates[0].Inode != 2 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if err := store.ConsumeCleanupPlan(ctx, plan.ID, plan.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadCleanupPlan(ctx, plan.ID, plan.Revision); !errors.Is(err, ErrCleanupPlanConsumed) {
		t.Fatalf("consumed error: %v", err)
	}
}

func TestCleanupPlanExpires(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 20, 21, 0, 0, 0, time.UTC)
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "state.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	plan, err := store.SaveCleanupPlan(ctx, cleanupcontrol.Plan{
		SchemaVersion: 1, ID: "plan_expiring", Scope: cleanupcontrol.Scope{Kind: "global"},
		Candidates: []cleanupcontrol.Candidate{}, Protected: []cleanupcontrol.Protection{},
		CreatedAt: now, ExpiresAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := store.ReadCleanupPlan(ctx, plan.ID, plan.Revision); !errors.Is(err, ErrCleanupPlanExpired) {
		t.Fatalf("expiration error: %v", err)
	}
}
