package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	cleanupcontrol "github.com/theronburger/switchyard/internal/control/cleanup"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/events"
	"github.com/theronburger/switchyard/internal/state"
)

// cleanupStoreHooks wraps the real store so tests can fail or interrupt the
// transaction at exact journal points without a test-only seam in the service.
type cleanupStoreHooks struct {
	*state.Store
	claim  func() error
	record func(cleanupcontrol.Claim) error
}

func (hooks cleanupStoreHooks) ClaimCleanupApply(ctx context.Context, id string, revision int64, candidateIDs []string) (cleanupcontrol.Plan, cleanupcontrol.Claim, error) {
	if hooks.claim != nil {
		if err := hooks.claim(); err != nil {
			return cleanupcontrol.Plan{}, cleanupcontrol.Claim{}, err
		}
	}
	return hooks.Store.ClaimCleanupApply(ctx, id, revision, candidateIDs)
}

func (hooks cleanupStoreHooks) RecordCleanupApply(ctx context.Context, claim cleanupcontrol.Claim) error {
	if err := hooks.Store.RecordCleanupApply(ctx, claim); err != nil {
		return err
	}
	if hooks.record != nil {
		return hooks.record(claim)
	}
	return nil
}

type blockingWorkspaceSource struct {
	entered chan struct{}
	release chan struct{}
}

func (source blockingWorkspaceSource) ListCurrent(ctx context.Context) ([]workspacecontrol.Result, error) {
	source.entered <- struct{}{}
	select {
	case <-source.release:
	case <-ctx.Done():
	}
	return nil, nil
}

type cleanupFixture struct {
	ctx         context.Context
	now         time.Time
	runtimeRoot string
	store       *state.Store
	owned       []string
	foreign     string
	foreignFile string
}

// newCleanupFixture lays out two stale owned preparations, one foreign
// (unmarked) preparation beside them, and a foreign file inside a step of the
// second owned candidate's sibling. Every test asserts the foreign resources
// survive regardless of how the transaction ends.
func newCleanupFixture(t *testing.T) *cleanupFixture {
	t.Helper()
	fixture := &cleanupFixture{
		ctx: context.Background(), now: time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC), runtimeRoot: t.TempDir(),
	}
	fixture.owned = []string{
		cleanupServicePreparation(t, fixture.runtimeRoot, "profile", "worktree_01", strings.Repeat("1", 64)),
		cleanupServicePreparation(t, fixture.runtimeRoot, "profile", "worktree_01", strings.Repeat("2", 64)),
	}
	fixture.foreign = filepath.Join(fixture.runtimeRoot, "repositories", "profile", "worktree_01", "preparation", strings.Repeat("f", 64), "install")
	if err := os.MkdirAll(fixture.foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.foreignFile = filepath.Join(fixture.foreign, "keep.txt")
	if err := os.WriteFile(fixture.foreignFile, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(fixture.ctx, state.Config{Path: filepath.Join(t.TempDir(), "state.sqlite"), Now: func() time.Time { return fixture.now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fixture.store = store
	return fixture
}

func (fixture *cleanupFixture) service(store CleanupStore, workspaces CleanupWorkspaceSource) *CleanupService {
	if workspaces == nil {
		workspaces = cleanupWorkspaceSource{}
	}
	return &CleanupService{
		Store: store, Workspaces: workspaces, RuntimeRoot: fixture.runtimeRoot,
		Now: func() time.Time { return fixture.now }, NewID: func() (string, error) { return "cleanup_plan_tx", nil },
	}
}

func (fixture *cleanupFixture) plan(t *testing.T, service *CleanupService) contractv2.CleanupPlan {
	t.Helper()
	plan, err := service.Plan(fixture.ctx, contractv2.CleanupPlanRequest{SchemaVersion: contractv2.SchemaVersion, Scope: contractv2.CleanupScope{Kind: "global"}})
	if err != nil || len(plan.Candidates) != 2 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	foreignProtected := false
	for _, protected := range plan.Protected {
		foreignProtected = foreignProtected || (protected.Path == filepath.Dir(fixture.foreign) && protected.Reason == "foreign-or-modified")
	}
	if !foreignProtected {
		t.Fatalf("foreign preparation is not reported as protected: %+v", plan.Protected)
	}
	return plan
}

// id returns the plan candidate that names the fixture's owned preparation
// at index; plan candidates are sorted by opaque ID, not by fixture order.
func (fixture *cleanupFixture) id(t *testing.T, plan contractv2.CleanupPlan, index int) string {
	t.Helper()
	for _, candidate := range plan.Candidates {
		if candidate.Path == fixture.owned[index] {
			return candidate.ID
		}
	}
	t.Fatalf("owned preparation %d is not a candidate", index)
	return ""
}

func (fixture *cleanupFixture) request(plan contractv2.CleanupPlan, ids ...string) contractv2.CleanupApplyRequest {
	if ids == nil {
		ids = fixture.orderedIDs(plan)
	}
	return contractv2.CleanupApplyRequest{SchemaVersion: contractv2.SchemaVersion, PlanID: plan.ID, ExpectedRevision: plan.Revision, CandidateIDs: ids}
}

func (fixture *cleanupFixture) orderedIDs(plan contractv2.CleanupPlan) []string {
	ids := make([]string, 0, len(fixture.owned))
	for _, path := range fixture.owned {
		for _, candidate := range plan.Candidates {
			if candidate.Path == path {
				ids = append(ids, candidate.ID)
			}
		}
	}
	return ids
}

func (fixture *cleanupFixture) assertForeignSurvives(t *testing.T) {
	t.Helper()
	if contents, err := os.ReadFile(fixture.foreignFile); err != nil || string(contents) != "foreign" {
		t.Fatalf("foreign resource changed: %q %v", contents, err)
	}
}

// cleanupAuditEvents returns every `cleanup.applied` event in the feed.
func (fixture *cleanupFixture) cleanupAuditEvents(t *testing.T) []events.Event {
	t.Helper()
	page, err := fixture.store.ReadEvents(fixture.ctx, 0, events.MaximumPageSize)
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

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestCleanupApplyMutatesNothingWhenTheClaimFails(t *testing.T) {
	fixture := newCleanupFixture(t)
	service := fixture.service(fixture.store, nil)
	plan := fixture.plan(t, service)
	claimFailure := errors.New("claim storage failed")
	failing := fixture.service(cleanupStoreHooks{Store: fixture.store, claim: func() error { return claimFailure }}, nil)
	if _, err := failing.Apply(fixture.ctx, fixture.request(plan)); !errors.Is(err, claimFailure) {
		t.Fatalf("claim failure: %v", err)
	}
	if !exists(fixture.owned[0]) || !exists(fixture.owned[1]) {
		t.Fatal("owned resources were removed without a claim")
	}
	fixture.assertForeignSurvives(t)
	// A claim that merely failed to be recorded left nothing behind: the
	// plan is still applicable once storage works again.
	result, err := service.Apply(fixture.ctx, fixture.request(plan))
	if err != nil || result.Attempts != 1 || !result.Removals[0].Removed || !result.Removals[1].Removed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if exists(fixture.owned[0]) || exists(fixture.owned[1]) {
		t.Fatal("owned candidates remain after a successful apply")
	}
	fixture.assertForeignSurvives(t)
}

func TestCleanupApplyRefusesAConcurrentApplyOfTheSamePlan(t *testing.T) {
	fixture := newCleanupFixture(t)
	plan := fixture.plan(t, fixture.service(fixture.store, nil))
	source := blockingWorkspaceSource{entered: make(chan struct{}, 1), release: make(chan struct{})}
	service := fixture.service(fixture.store, source)
	var wait sync.WaitGroup
	wait.Add(1)
	var first contractv2.CleanupResult
	var firstErr error
	go func() {
		defer wait.Done()
		first, firstErr = service.Apply(fixture.ctx, fixture.request(plan))
	}()
	<-source.entered
	if _, err := service.Apply(fixture.ctx, fixture.request(plan)); !errors.Is(err, ErrCleanupApplyInProgress) {
		t.Fatalf("concurrent apply: %v", err)
	}
	if !exists(fixture.owned[0]) || !exists(fixture.owned[1]) {
		t.Fatal("a refused concurrent apply removed resources")
	}
	close(source.release)
	wait.Wait()
	if firstErr != nil || !first.Removals[0].Removed || !first.Removals[1].Removed || first.Attempts != 1 {
		t.Fatalf("first=%+v err=%v", first, firstErr)
	}
	fixture.assertForeignSurvives(t)
}

func TestCleanupApplyRefusesAnotherDaemonsClaim(t *testing.T) {
	fixture := newCleanupFixture(t)
	service := fixture.service(fixture.store, nil)
	plan := fixture.plan(t, service)
	// Another daemon process claimed this revision for one candidate and
	// has not finished. This process must neither hijack nor widen it.
	if _, _, err := fixture.store.ClaimCleanupApply(fixture.ctx, plan.ID, plan.Revision, []string{fixture.id(t, plan, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(fixture.ctx, fixture.request(plan)); !errors.Is(err, state.ErrCleanupApplyMismatch) {
		t.Fatalf("widened apply: %v", err)
	}
	if !exists(fixture.owned[0]) || !exists(fixture.owned[1]) {
		t.Fatal("a mismatched apply removed resources")
	}
	// The same request resumes that claim deterministically.
	result, err := service.Apply(fixture.ctx, fixture.request(plan, fixture.id(t, plan, 0)))
	if err != nil || result.Attempts != 2 || len(result.Removals) != 1 || !result.Removals[0].Removed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if exists(fixture.owned[0]) || !exists(fixture.owned[1]) {
		t.Fatal("resumed claim did not act on exactly its candidate")
	}
	fixture.assertForeignSurvives(t)
}

func TestCleanupApplyRepresentsInterruptionAndResumesExactly(t *testing.T) {
	fixture := newCleanupFixture(t)
	service := fixture.service(fixture.store, nil)
	plan := fixture.plan(t, service)
	second := fixture.id(t, plan, 1)

	// The daemon dies (the request context is torn down) right after the
	// second candidate is journaled as in flight and before it is touched.
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	crashing := fixture.service(cleanupStoreHooks{Store: fixture.store, record: func(claim cleanupcontrol.Claim) error {
		if claim.InFlight == second {
			cancel()
		}
		return nil
	}}, nil)
	if _, err := crashing.Apply(ctx, fixture.request(plan)); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted apply: %v", err)
	}
	if exists(fixture.owned[0]) || !exists(fixture.owned[1]) {
		t.Fatal("interruption did not stop exactly between candidates")
	}
	// An incomplete apply is not a completed one: nothing is audited yet.
	if applied := fixture.cleanupAuditEvents(t); len(applied) != 0 {
		t.Fatalf("interrupted apply emitted completion audit: %+v", applied)
	}
	// The plan is not consumed and not silently applicable as if untouched:
	// a different request is refused, and the journal names the in-flight
	// candidate.
	if _, _, err := fixture.store.ClaimCleanupApply(fixture.ctx, plan.ID, plan.Revision, []string{second}); !errors.Is(err, state.ErrCleanupApplyMismatch) {
		t.Fatalf("narrowed retry: %v", err)
	}

	// A fresh daemon retries the identical request after the plan expired:
	// the already-removed candidate is replayed from the journal, the
	// in-flight one is finished now, and the attempt count is truthful.
	fixture.now = fixture.now.Add(cleanupPlanLifetime + time.Minute)
	restarted := fixture.service(fixture.store, nil)
	result, err := restarted.Apply(fixture.ctx, fixture.request(plan))
	if err != nil || result.Attempts != 2 || !result.ClaimedAt.Equal(time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)) ||
		!result.Removals[0].Removed || !result.Removals[1].Removed || result.Validate() != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if exists(fixture.owned[1]) {
		t.Fatal("resumed candidate remains")
	}
	// Completion is audited exactly once, with identifiers and counts only.
	applied := fixture.cleanupAuditEvents(t)
	if len(applied) != 1 {
		t.Fatalf("completed apply audit events: %+v", applied)
	}
	var payload events.CleanupAuditPayload
	if err := json.Unmarshal(applied[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload != (events.CleanupAuditPayload{PlanID: plan.ID, PlanRevision: plan.Revision, Attempts: 2, Requested: 2, Removed: 2}) {
		t.Fatalf("audit payload: %+v", payload)
	}
	for _, candidate := range plan.Candidates {
		if strings.Contains(string(applied[0].Payload), candidate.Path) || strings.Contains(string(applied[0].Payload), candidate.ID) {
			t.Fatalf("audit payload carries candidate identity: %s", applied[0].Payload)
		}
	}
	if strings.Contains(string(applied[0].Payload), fixture.runtimeRoot) || strings.Contains(string(applied[0].Payload), "profile") {
		t.Fatalf("audit payload carries a path or profile: %s", applied[0].Payload)
	}
	// Retrying the completed request replays the identical result without
	// a second audit event.
	replay, err := restarted.Apply(fixture.ctx, fixture.request(plan))
	if err != nil || replay.Attempts != 2 || !replay.CompletedAt.Equal(result.CompletedAt) || len(replay.Removals) != 2 {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if applied := fixture.cleanupAuditEvents(t); len(applied) != 1 {
		t.Fatalf("replay duplicated the completion audit: %+v", applied)
	}
	if _, err := restarted.Apply(fixture.ctx, fixture.request(plan, second)); !errors.Is(err, state.ErrCleanupPlanConsumed) {
		t.Fatalf("different request after completion: %v", err)
	}
	fixture.assertForeignSurvives(t)
}

func TestCleanupApplyReportsAnInFlightCandidateThatChangedAsInterrupted(t *testing.T) {
	fixture := newCleanupFixture(t)
	service := fixture.service(fixture.store, nil)
	plan := fixture.plan(t, service)
	first := fixture.id(t, plan, 0)
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	crashing := fixture.service(cleanupStoreHooks{Store: fixture.store, record: func(claim cleanupcontrol.Claim) error {
		if claim.InFlight == first {
			cancel()
		}
		return nil
	}}, nil)
	if _, err := crashing.Apply(ctx, fixture.request(plan)); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted apply: %v", err)
	}
	// Before the retry, something foreign appears inside the in-flight
	// candidate. The retry must not remove it and must not call it a plain
	// protection: this candidate's removal was started and did not finish.
	foreign := filepath.Join(fixture.owned[0], "install", "foreign.txt")
	if err := os.WriteFile(foreign, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service(fixture.store, nil).Apply(fixture.ctx, fixture.request(plan))
	if err != nil || result.Attempts != 2 ||
		result.Removals[0] != (contractv2.CleanupRemoval{CandidateID: first, Reason: "interrupted"}) ||
		!result.Removals[1].Removed || result.Validate() != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if contents, err := os.ReadFile(foreign); err != nil || string(contents) != "preserve" {
		t.Fatalf("foreign file inside an interrupted candidate changed: %q %v", contents, err)
	}
	fixture.assertForeignSurvives(t)
}

func TestCleanupApplyResumesAPartiallyRemovedStep(t *testing.T) {
	fixture := newCleanupFixture(t)
	service := fixture.service(fixture.store, nil)
	plan := fixture.plan(t, service)
	first := fixture.id(t, plan, 0)
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	crashing := fixture.service(cleanupStoreHooks{Store: fixture.store, record: func(claim cleanupcontrol.Claim) error {
		if claim.InFlight == first {
			cancel()
		}
		return nil
	}}, nil)
	if _, err := crashing.Apply(ctx, fixture.request(plan)); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted apply: %v", err)
	}
	// Removal deletes logs before the ownership marker, so a crash inside a
	// step leaves it still positively owned. Simulate exactly that state.
	if err := os.Remove(filepath.Join(fixture.owned[0], "install", "stdout.log")); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service(fixture.store, nil).Apply(fixture.ctx, fixture.request(plan))
	if err != nil || !result.Removals[0].Removed || !result.Removals[1].Removed {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if exists(fixture.owned[0]) || exists(fixture.owned[1]) {
		t.Fatal("owned candidates remain")
	}
	fixture.assertForeignSurvives(t)
}
