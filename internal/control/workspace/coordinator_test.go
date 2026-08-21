package workspace

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestCoordinatorEnsuresOnceAndReusesVerifiedFingerprint(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	journal := newMemoryJournal()
	runner := &recordingRunner{}
	verifier := &recordingVerifier{}
	planner := staticWorkspacePlanner{plan: validWorkspacePlan(t)}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Planner: planner, Runner: runner, Verifier: verifier,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := EnsureRequest{OperationID: "operation_01", WorktreeID: "worktree_01"}
	first, err := coordinator.Ensure(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != StateReady || !first.PreparedAt.Equal(now) ||
		!reflect.DeepEqual(runner.steps, []string{"hydrate"}) {
		t.Fatalf("first ensure: result=%+v steps=%v", first, runner.steps)
	}
	request.OperationID = "operation_02"
	second, err := coordinator.Ensure(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(runner.steps, []string{"hydrate"}) {
		t.Fatalf("cached ensure reran work: result=%+v steps=%v", second, runner.steps)
	}
	if verifier.calls != 2 {
		t.Fatalf("requirements were not verified on both starts: %d", verifier.calls)
	}
}

func TestCoordinatorFingerprintChangeRunsNewPlan(t *testing.T) {
	t.Parallel()
	journal := newMemoryJournal()
	runner := &recordingRunner{}
	planner := &mutableWorkspacePlanner{plan: validWorkspacePlan(t)}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Planner: planner, Runner: runner, Verifier: &recordingVerifier{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Ensure(context.Background(), EnsureRequest{
		OperationID: "operation_01", WorktreeID: "worktree_01",
	}); err != nil {
		t.Fatal(err)
	}
	planner.plan.Fingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := coordinator.Ensure(context.Background(), EnsureRequest{
		OperationID: "operation_02", WorktreeID: "worktree_01",
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.steps, []string{"hydrate", "hydrate"}) {
		t.Fatalf("changed plan did not rerun: %v", runner.steps)
	}
}

func TestCoordinatorPersistsFailureWithoutPublishingReady(t *testing.T) {
	t.Parallel()
	journal := newMemoryJournal()
	runner := &recordingRunner{err: errors.New("contains secret@example.invalid")}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Planner: staticWorkspacePlanner{plan: validWorkspacePlan(t)},
		Runner: runner, Verifier: &recordingVerifier{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = coordinator.Ensure(context.Background(), EnsureRequest{
		OperationID: "operation_01", WorktreeID: "worktree_01",
	})
	if !errors.Is(err, ErrStepFailed) {
		t.Fatalf("ensure error: %v", err)
	}
	if journal.current != nil || len(journal.records) != 1 ||
		journal.records[0].State != StateFailed || journal.records[0].FailureCode != "WORKSPACE_STEP_FAILED" {
		t.Fatalf("failure was not persisted safely: current=%+v records=%+v", journal.current, journal.records)
	}
}

func TestCoordinatorReconcileFailsInterruptedWorkWithoutRerunning(t *testing.T) {
	t.Parallel()
	journal := newMemoryJournal()
	journal.records = []OperationRecord{{
		OperationID: "operation_01", WorktreeID: "worktree_01", State: StateRunning,
		Phase: PhasePreparing, Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		StepCount: 2, NextStep: 1,
	}}
	runner := &recordingRunner{}
	coordinator, err := NewCoordinator(Config{
		Journal: journal, Planner: staticWorkspacePlanner{plan: validWorkspacePlan(t)},
		Runner: runner, Verifier: &recordingVerifier{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.steps) != 0 || journal.records[0].State != StateFailed ||
		journal.records[0].FailureCode != "WORKSPACE_PREPARATION_INTERRUPTED" {
		t.Fatalf("interrupted work was not safely failed: records=%+v steps=%v", journal.records, runner.steps)
	}
}

func validWorkspacePlan(t *testing.T) Plan {
	t.Helper()
	return Plan{
		WorktreeID: "worktree_01", ProfileKey: "example", WorktreeRoot: "/tmp/worktree",
		Ownership:   OwnershipAdopted,
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Steps: []StepSpec{{
			ID: "hydrate", Executable: "/usr/bin/true", Arguments: []string{"--exact"},
			Environment: []string{"HOME=/tmp"}, Directory: "/tmp/worktree",
			RunDirectory: "/tmp/run", Timeout: time.Minute,
		}},
		Requirements: []Requirement{{ID: "root", Path: "/tmp/worktree", Kind: RequirementDirectory}},
		Toolchains: []Toolchain{{
			ID: "go", RequestedVersion: "1.26", ResolvedVersion: "1.26.5", Executable: "/usr/bin/go",
		}},
	}
}

type staticWorkspacePlanner struct{ plan Plan }

func (planner staticWorkspacePlanner) Build(PlanningRequest) (Plan, error) { return planner.plan, nil }

type mutableWorkspacePlanner struct{ plan Plan }

func (planner *mutableWorkspacePlanner) Build(PlanningRequest) (Plan, error) {
	return planner.plan, nil
}

type recordingRunner struct {
	mutex sync.Mutex
	steps []string
	err   error
}

func (runner *recordingRunner) Run(_ context.Context, step StepSpec) error {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	runner.steps = append(runner.steps, step.ID)
	return runner.err
}

type recordingVerifier struct {
	mutex sync.Mutex
	calls int
	err   error
}

func (verifier *recordingVerifier) Verify(context.Context, Plan) error {
	verifier.mutex.Lock()
	defer verifier.mutex.Unlock()
	verifier.calls++
	return verifier.err
}

type memoryJournal struct {
	mutex   sync.Mutex
	records []OperationRecord
	current *Result
}

func newMemoryJournal() *memoryJournal { return &memoryJournal{} }

func (journal *memoryJournal) Begin(_ context.Context, record OperationRecord) error {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	journal.records = append(journal.records, record)
	return nil
}

func (journal *memoryJournal) Update(_ context.Context, record OperationRecord) error {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	for index := len(journal.records) - 1; index >= 0; index-- {
		if journal.records[index].OperationID == record.OperationID {
			journal.records[index] = record
			return nil
		}
	}
	return errors.New("record missing")
}

func (journal *memoryJournal) Publish(_ context.Context, record OperationRecord, result Result) error {
	if err := journal.Update(context.Background(), record); err != nil {
		return err
	}
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	copy := result
	journal.current = &copy
	return nil
}

func (journal *memoryJournal) Current(_ context.Context, worktreeID string) (Result, bool, error) {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	if journal.current == nil || journal.current.WorktreeID != worktreeID {
		return Result{}, false, nil
	}
	return *journal.current, true, nil
}

func (journal *memoryJournal) Incomplete(context.Context) ([]OperationRecord, error) {
	journal.mutex.Lock()
	defer journal.mutex.Unlock()
	result := make([]OperationRecord, 0)
	for _, record := range journal.records {
		if record.State == StatePending || record.State == StateRunning {
			result = append(result, record)
		}
	}
	return result, nil
}
