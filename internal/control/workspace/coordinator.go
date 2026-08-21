package workspace

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Config struct {
	Journal  Journal
	Planner  PlanBuilder
	Runner   StepRunner
	Verifier RequirementVerifier
	Now      func() time.Time
}

type Coordinator struct {
	journal  Journal
	planner  PlanBuilder
	runner   StepRunner
	verifier RequirementVerifier
	now      func() time.Time
	locks    sync.Map
}

func NewCoordinator(config Config) (*Coordinator, error) {
	if config.Journal == nil || config.Planner == nil || config.Runner == nil || config.Verifier == nil {
		return nil, ErrInvalidRequest
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Coordinator{
		journal: config.Journal, planner: config.Planner, runner: config.Runner,
		verifier: config.Verifier, now: config.Now,
	}, nil
}

func (coordinator *Coordinator) Ensure(ctx context.Context, request EnsureRequest) (Result, error) {
	if ctx == nil || !idPattern.MatchString(request.OperationID) || !idPattern.MatchString(request.WorktreeID) {
		return Result{}, ErrInvalidRequest
	}
	lockValue, _ := coordinator.locks.LoadOrStore(request.WorktreeID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	plan, err := coordinator.planner.Build(PlanningRequest(request))
	if err != nil || validatePlan(plan) != nil || plan.WorktreeID != request.WorktreeID {
		return Result{}, ErrInvalidPlan
	}
	current, found, err := coordinator.journal.Current(ctx, request.WorktreeID)
	if err != nil {
		return Result{}, err
	}
	if found && current.State == StateReady && current.Fingerprint == plan.Fingerprint &&
		coordinator.verifier.Verify(ctx, plan) == nil {
		return current, nil
	}

	record := OperationRecord{
		OperationID: request.OperationID, WorktreeID: request.WorktreeID,
		State: StatePending, Phase: PhasePending, Fingerprint: plan.Fingerprint,
		StepCount: len(plan.Steps),
	}
	if err := coordinator.journal.Begin(ctx, record); err != nil {
		return Result{}, err
	}
	record.State = StateRunning
	for index, step := range plan.Steps {
		record.Phase = PhasePreparing
		record.NextStep = index
		if err := coordinator.journal.Update(ctx, record); err != nil {
			return Result{}, err
		}
		if err := coordinator.runner.Run(ctx, step); err != nil {
			return Result{}, coordinator.fail(record, "WORKSPACE_STEP_FAILED", errors.Join(ErrStepFailed, err))
		}
		record.NextStep = index + 1
	}
	record.Phase = PhaseVerifying
	if err := coordinator.journal.Update(ctx, record); err != nil {
		return Result{}, err
	}
	if err := coordinator.verifier.Verify(ctx, plan); err != nil {
		return Result{}, coordinator.fail(record, "WORKSPACE_NOT_READY", errors.Join(ErrNotReady, err))
	}
	result := Result{
		WorktreeID: plan.WorktreeID, ProfileKey: plan.ProfileKey, WorktreeRoot: plan.WorktreeRoot,
		Ownership: plan.Ownership, State: StateReady, Fingerprint: plan.Fingerprint,
		Toolchains: append([]Toolchain(nil), plan.Toolchains...), PreparedAt: coordinator.now().UTC(),
	}
	record.State = StateReady
	record.Phase = PhaseComplete
	record.NextStep = record.StepCount
	if err := coordinator.journal.Publish(ctx, record, result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func (coordinator *Coordinator) fail(record OperationRecord, code string, cause error) error {
	record.State = StateFailed
	record.Phase = PhaseComplete
	record.FailureCode = code
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := coordinator.journal.Update(cleanup, record); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (coordinator *Coordinator) Reconcile(ctx context.Context) error {
	return FailInterruptedPreparations(ctx, coordinator.journal)
}

// FailInterruptedPreparations marks every incomplete preparation record as
// failed. It runs on every daemon boot, whether or not a coordinator exists
// for the worktree: an incomplete record left behind would both wedge the
// worktree (only one incomplete record per worktree may exist) and desync
// from its public operation, which boot fails separately.
func FailInterruptedPreparations(ctx context.Context, journal Journal) error {
	records, err := journal.Incomplete(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		record.State = StateFailed
		record.Phase = PhaseComplete
		record.FailureCode = "WORKSPACE_PREPARATION_INTERRUPTED"
		if err := journal.Update(ctx, record); err != nil {
			return err
		}
	}
	return nil
}
