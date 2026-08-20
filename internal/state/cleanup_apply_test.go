package state

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	cleanupcontrol "github.com/theronburger/switchyard/internal/control/cleanup"
	"github.com/theronburger/switchyard/internal/events"
)

func savedCleanupPlan(ctx context.Context, t *testing.T, store *Store, id string, now time.Time, lifetime time.Duration) cleanupcontrol.Plan {
	t.Helper()
	plan, err := store.SaveCleanupPlan(ctx, cleanupcontrol.Plan{
		SchemaVersion: 1, ID: id, Scope: cleanupcontrol.Scope{Kind: "global"},
		Candidates: []cleanupcontrol.Candidate{{ID: "candidate_01", Path: "/private/one"}, {ID: "candidate_02", Path: "/private/two"}},
		Protected:  []cleanupcontrol.Protection{}, CreatedAt: now, ExpiresAt: now.Add(lifetime),
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestCleanupClaimIsAtomicExactAndResumable(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "state.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	plan := savedCleanupPlan(ctx, t, store, "plan_claim", now, time.Minute)
	requested := []string{"candidate_02", "candidate_01"}

	if _, _, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision+1, requested); !errors.Is(err, ErrCleanupPlanNotFound) {
		t.Fatalf("stale revision: %v", err)
	}
	if _, _, err := store.ClaimCleanupApply(ctx, "plan_other", plan.Revision, requested); !errors.Is(err, ErrCleanupPlanNotFound) {
		t.Fatalf("unknown plan: %v", err)
	}
	// A claim is refused before any resource is touched, so journaling
	// against an unclaimed plan is an error rather than a silent no-op.
	unclaimed, _ := cleanupcontrol.NewClaim(plan.ID, plan.Revision, requested, now)
	if err := store.RecordCleanupApply(ctx, unclaimed); !errors.Is(err, ErrCleanupApplyStale) {
		t.Fatalf("journal without claim: %v", err)
	}

	storedPlan, claim, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, requested)
	if err != nil || claim.Attempts != 1 || !claim.ClaimedAt.Equal(now) || storedPlan.Candidates[1].Path != "/private/two" {
		t.Fatalf("claim=%+v plan=%+v err=%v", claim, storedPlan, err)
	}
	// The claim, not the revision check, is now the authorization: a second
	// request for the same revision with any other candidate list is refused.
	for name, other := range map[string][]string{
		"reordered": {"candidate_01", "candidate_02"},
		"subset":    {"candidate_02"},
		"empty":     {},
	} {
		if _, _, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, other); !errors.Is(err, ErrCleanupApplyMismatch) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	// Progress survives and an identical retry resumes it with the same
	// claim time and an incremented attempt count, even after expiry.
	claim, err = claim.Begin("candidate_02")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCleanupApply(ctx, claim); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	_, resumed, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, requested)
	if err != nil || resumed.Attempts != 2 || resumed.InFlight != "candidate_02" || !resumed.ClaimedAt.Equal(claim.ClaimedAt) {
		t.Fatalf("resumed=%+v err=%v", resumed, err)
	}
	// An incomplete claim pins its expired plan against pruning.
	savedCleanupPlan(ctx, t, store, "plan_later", now, time.Minute)
	if _, _, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, requested); err != nil {
		t.Fatalf("incomplete claim pruned: %v", err)
	}

	resumed, err = resumed.Finish(cleanupcontrol.Removal{CandidateID: "candidate_02", Removed: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCleanupApply(ctx, resumed); !errors.Is(err, cleanupcontrol.ErrClaimInvalid) {
		t.Fatalf("completion with a missing outcome: %v", err)
	}
	resumed, err = resumed.Finish(cleanupcontrol.Removal{CandidateID: "candidate_01", Reason: cleanupcontrol.ReasonInterrupted})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := resumed.Complete(now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCleanupApply(ctx, completed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadCleanupPlan(ctx, plan.ID, plan.Revision); !errors.Is(err, ErrCleanupPlanConsumed) {
		t.Fatalf("plan after completion: %v", err)
	}
	// Completion is final: stale journal writes and re-completion are refused.
	if err := store.RecordCleanupApply(ctx, resumed); !errors.Is(err, ErrCleanupApplyStale) {
		t.Fatalf("journal after completion: %v", err)
	}
	if err := store.CompleteCleanupApply(ctx, completed); !errors.Is(err, ErrCleanupApplyStale) {
		t.Fatalf("second completion: %v", err)
	}
	// The identical request replays the recorded outcome; anything else is
	// told the plan is consumed.
	_, replay, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, requested)
	if err != nil || !replay.Completed() || replay.Attempts != 2 || len(replay.Outcomes) != 2 {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if _, _, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, []string{"candidate_01"}); !errors.Is(err, ErrCleanupPlanConsumed) {
		t.Fatalf("consumed: %v", err)
	}
}

func TestCleanupClaimRequiresAnUnexpiredPlan(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "state.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	plan := savedCleanupPlan(ctx, t, store, "plan_expiring", now, time.Second)
	now = now.Add(time.Second)
	if _, _, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, []string{"candidate_01"}); !errors.Is(err, ErrCleanupPlanExpired) {
		t.Fatalf("expired claim: %v", err)
	}
	// An unclaimed expired plan is pruned by the next plan; a consumed,
	// unexpired plan is retained so its result can still be replayed.
	fresh := savedCleanupPlan(ctx, t, store, "plan_fresh", now, time.Minute)
	if _, _, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, []string{"candidate_01"}); !errors.Is(err, ErrCleanupPlanNotFound) {
		t.Fatalf("pruned plan: %v", err)
	}
	_, claim, err := store.ClaimCleanupApply(ctx, fresh.ID, fresh.Revision, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if claim, err = claim.Complete(now); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCleanupApply(ctx, claim); err != nil {
		t.Fatal(err)
	}
	savedCleanupPlan(ctx, t, store, "plan_after", now, time.Minute)
	if _, replay, err := store.ClaimCleanupApply(ctx, fresh.ID, fresh.Revision, []string{}); err != nil || !replay.Completed() {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func cleanupAppliedEvents(ctx context.Context, t *testing.T, store *Store) []events.Event {
	t.Helper()
	page, err := store.ReadEvents(ctx, 0, events.MaximumPageSize)
	if err != nil {
		t.Fatal(err)
	}
	var applied []events.Event
	for _, event := range page.Events {
		if event.Kind == events.KindCleanupApplied {
			applied = append(applied, event)
		}
	}
	return applied
}

func TestCleanupCompletionAuditIsTransactionalAndEmittedOnce(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	store, err := Open(ctx, Config{Path: filepath.Join(t.TempDir(), "state.sqlite"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.CommitSnapshot(ctx, auditSnapshot()); err != nil {
		t.Fatal(err)
	}
	plan := savedCleanupPlan(ctx, t, store, "plan_audit", now, time.Minute)
	requested := []string{"candidate_01", "candidate_02"}
	_, claim, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, requested)
	if err != nil {
		t.Fatal(err)
	}
	if claim, err = claim.Begin("candidate_01"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCleanupApply(ctx, claim); err != nil {
		t.Fatal(err)
	}
	// Claiming and journaling progress are not completion: no event yet.
	if applied := cleanupAppliedEvents(ctx, t, store); len(applied) != 0 {
		t.Fatalf("incomplete claim emitted completion audit: %+v", applied)
	}
	if claim, err = claim.Finish(cleanupcontrol.Removal{CandidateID: "candidate_01", Removed: true}); err != nil {
		t.Fatal(err)
	}
	if claim, err = claim.Finish(cleanupcontrol.Removal{CandidateID: "candidate_02", Reason: cleanupcontrol.ReasonInterrupted}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCleanupApply(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if applied := cleanupAppliedEvents(ctx, t, store); len(applied) != 0 {
		t.Fatalf("final outcomes without completion emitted audit: %+v", applied)
	}
	completed, err := claim.Complete(now)
	if err != nil {
		t.Fatal(err)
	}

	// If the audit event cannot be appended, neither the completed claim
	// nor the consumed plan may commit: the apply stays resumable.
	if _, err := store.database.ExecContext(ctx, `
CREATE TRIGGER refuse_cleanup_audit BEFORE INSERT ON events
WHEN NEW.kind = 'cleanup.applied'
BEGIN SELECT RAISE(ABORT, 'audit refused'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCleanupApply(ctx, completed); err == nil || !containsAny(err.Error(), "audit refused") {
		t.Fatalf("completion with a failing audit append: %v", err)
	}
	if _, err := store.ReadCleanupPlan(ctx, plan.ID, plan.Revision); err != nil {
		t.Fatalf("plan consumed despite rolled-back completion: %v", err)
	}
	_, resumed, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, requested)
	if err != nil || resumed.Completed() || resumed.Attempts != 2 || resumed.InFlight != "" || len(resumed.Outcomes) != 2 {
		t.Fatalf("claim after rolled-back completion: %+v err=%v", resumed, err)
	}
	if applied := cleanupAppliedEvents(ctx, t, store); len(applied) != 0 {
		t.Fatalf("rolled-back completion left an event: %+v", applied)
	}
	if _, err := store.database.ExecContext(ctx, `DROP TRIGGER refuse_cleanup_audit`); err != nil {
		t.Fatal(err)
	}

	if completed, err = resumed.Complete(now); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteCleanupApply(ctx, completed); err != nil {
		t.Fatal(err)
	}
	applied := cleanupAppliedEvents(ctx, t, store)
	if len(applied) != 1 || applied[0].Revision <= 0 || applied[0].EnvironmentID != "" {
		t.Fatalf("completion audit: %+v", applied)
	}
	var payload events.CleanupAuditPayload
	if err := decodeStrict(applied[0].Payload, &payload); err != nil {
		t.Fatalf("payload %s: %v", applied[0].Payload, err)
	}
	want := events.CleanupAuditPayload{PlanID: plan.ID, PlanRevision: plan.Revision, Attempts: 2, Requested: 2, Removed: 1, Interrupted: 1}
	if payload != want {
		t.Fatalf("payload=%+v want=%+v", payload, want)
	}
	if containsAny(string(applied[0].Payload), "/private/", "candidate_0") {
		t.Fatalf("audit payload carries candidate identity: %s", applied[0].Payload)
	}
	// Re-completion is refused and appends nothing more.
	if err := store.CompleteCleanupApply(ctx, completed); !errors.Is(err, ErrCleanupApplyStale) {
		t.Fatalf("second completion: %v", err)
	}
	if _, replay, err := store.ClaimCleanupApply(ctx, plan.ID, plan.Revision, requested); err != nil || !replay.Completed() {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if applied := cleanupAppliedEvents(ctx, t, store); len(applied) != 1 {
		t.Fatalf("completion audited more than once: %+v", applied)
	}
}
