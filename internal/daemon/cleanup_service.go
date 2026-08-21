package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	cleanupcontrol "github.com/theronburger/switchyard/internal/control/cleanup"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/state"
)

const cleanupPlanLifetime = 10 * time.Minute

// ErrCleanupApplyInProgress reports that this daemon is already applying the
// same plan; the second request is refused without touching anything.
var ErrCleanupApplyInProgress = errors.New("cleanup apply is already in progress")

type CleanupStore interface {
	SaveCleanupPlan(context.Context, cleanupcontrol.Plan) (cleanupcontrol.Plan, error)
	// ClaimCleanupApply atomically records authorization for one plan
	// revision and candidate list before any mutation, resuming or replaying
	// an identical earlier request and refusing any other.
	ClaimCleanupApply(context.Context, string, int64, []string) (cleanupcontrol.Plan, cleanupcontrol.Claim, error)
	RecordCleanupApply(context.Context, cleanupcontrol.Claim) error
	CompleteCleanupApply(context.Context, cleanupcontrol.Claim) error
}

type CleanupWorkspaceSource interface {
	ListCurrent(context.Context) ([]workspacecontrol.Result, error)
}

type CleanupService struct {
	Store       CleanupStore
	Workspaces  CleanupWorkspaceSource
	RuntimeRoot string
	Now         func() time.Time
	NewID       func() (string, error)

	mutex  sync.Mutex
	active map[string]struct{}
}

func (service *CleanupService) Plan(ctx context.Context, request contractv2.CleanupPlanRequest) (contractv2.CleanupPlan, error) {
	if request.Validate() != nil || service.Store == nil || service.Workspaces == nil || service.RuntimeRoot == "" {
		return contractv2.CleanupPlan{}, errors.New("cleanup service is unavailable")
	}
	planner, err := service.planner(ctx)
	if err != nil {
		return contractv2.CleanupPlan{}, err
	}
	inventory, err := planner.Inventory(ctx, cleanupcontrol.Scope{Kind: request.Scope.Kind, ID: request.Scope.ID})
	if err != nil {
		return contractv2.CleanupPlan{}, err
	}
	newID := service.NewID
	if newID == nil {
		newID = newCleanupPlanID
	}
	id, err := newID()
	if err != nil {
		return contractv2.CleanupPlan{}, err
	}
	now := service.now()
	plan, err := service.Store.SaveCleanupPlan(ctx, cleanupcontrol.Plan{
		SchemaVersion: 1, ID: id, Scope: cleanupcontrol.Scope{Kind: request.Scope.Kind, ID: request.Scope.ID},
		Candidates: inventory.Candidates, Protected: inventory.Protected,
		CreatedAt: now, ExpiresAt: now.Add(cleanupPlanLifetime),
	})
	if err != nil {
		return contractv2.CleanupPlan{}, err
	}
	return cleanupPlanContract(plan), nil
}

// Apply is a claimed transaction. The order is fixed: read-only revalidation
// inputs, an atomic durable claim of this exact request, then per-candidate
// removal with every outcome journaled before the next candidate starts, then
// completion that consumes the plan together with its recorded result. A
// request that loses the claim never mutates anything; a request that repeats
// a claimed request resumes exactly where the earlier attempt stopped; a
// request that repeats a completed request replays the recorded result.
func (service *CleanupService) Apply(ctx context.Context, request contractv2.CleanupApplyRequest) (contractv2.CleanupResult, error) {
	if request.Validate() != nil || service.Store == nil || service.Workspaces == nil {
		return contractv2.CleanupResult{}, errors.New("cleanup service is unavailable")
	}
	if !service.acquire(request.PlanID) {
		return contractv2.CleanupResult{}, ErrCleanupApplyInProgress
	}
	defer service.release(request.PlanID)
	planner, err := service.planner(ctx)
	if err != nil {
		return contractv2.CleanupResult{}, err
	}
	plan, claim, err := service.Store.ClaimCleanupApply(ctx, request.PlanID, request.ExpectedRevision, request.CandidateIDs)
	if err != nil {
		return contractv2.CleanupResult{}, err
	}
	if claim.Completed() {
		return cleanupResultContract(claim)
	}
	byID := make(map[string]cleanupcontrol.Candidate, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		byID[candidate.ID] = candidate
	}
	for _, id := range claim.CandidateIDs {
		if _, recorded := claim.Outcome(id); recorded {
			continue
		}
		candidate, found := byID[id]
		if !found {
			if claim, err = service.finish(ctx, claim, cleanupcontrol.Removal{CandidateID: id, Reason: cleanupcontrol.ReasonNotInPlan}); err != nil {
				return contractv2.CleanupResult{}, err
			}
			continue
		}
		resumed := claim.InFlight == id
		if claim, err = claim.Begin(id); err != nil {
			return contractv2.CleanupResult{}, err
		}
		if err := service.Store.RecordCleanupApply(ctx, claim); err != nil {
			return contractv2.CleanupResult{}, err
		}
		removal := cleanupcontrol.Removal{CandidateID: id}
		switch removeErr := planner.Remove(ctx, candidate); {
		case removeErr == nil:
			removal.Removed = true
		case ctx.Err() != nil:
			// Cancelled before the outcome is known: the journal keeps the
			// candidate in flight so a retry resumes it truthfully.
			return contractv2.CleanupResult{}, ctx.Err()
		case resumed && errors.Is(removeErr, cleanupcontrol.ErrCandidateChanged):
			// An earlier attempt started this candidate and it no longer
			// matches the plan: report the interruption, not a protection.
			removal.Reason = cleanupcontrol.ReasonInterrupted
		case errors.Is(removeErr, cleanupcontrol.ErrCandidateChanged), errors.Is(removeErr, cleanupcontrol.ErrProtectedResource):
			removal.Reason = cleanupcontrol.ReasonChangedOrProtected
		default:
			// The filesystem failed part-way through a positively owned
			// candidate. Its remaining files are still owned and re-plannable.
			removal.Reason = cleanupcontrol.ReasonInterrupted
		}
		if claim, err = service.finish(ctx, claim, removal); err != nil {
			return contractv2.CleanupResult{}, err
		}
	}
	if claim, err = claim.Complete(service.now()); err != nil {
		return contractv2.CleanupResult{}, err
	}
	if err := service.Store.CompleteCleanupApply(ctx, claim); err != nil {
		return contractv2.CleanupResult{}, err
	}
	return cleanupResultContract(claim)
}

func (service *CleanupService) finish(ctx context.Context, claim cleanupcontrol.Claim, removal cleanupcontrol.Removal) (cleanupcontrol.Claim, error) {
	claim, err := claim.Finish(removal)
	if err != nil {
		return cleanupcontrol.Claim{}, err
	}
	if err := service.Store.RecordCleanupApply(ctx, claim); err != nil {
		return cleanupcontrol.Claim{}, err
	}
	return claim, nil
}

func (service *CleanupService) acquire(planID string) bool {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.active == nil {
		service.active = make(map[string]struct{})
	}
	if _, busy := service.active[planID]; busy {
		return false
	}
	service.active[planID] = struct{}{}
	return true
}

func (service *CleanupService) release(planID string) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	delete(service.active, planID)
}

func (service *CleanupService) planner(ctx context.Context) (cleanupcontrol.PrivatePreparationPlanner, error) {
	results, err := service.Workspaces.ListCurrent(ctx)
	if err != nil {
		return cleanupcontrol.PrivatePreparationPlanner{}, err
	}
	current := make(map[string]string, len(results))
	for _, result := range results {
		current[result.WorktreeID] = result.Fingerprint
	}
	return cleanupcontrol.PrivatePreparationPlanner{RuntimeRoot: service.RuntimeRoot, CurrentFingerprints: current}, nil
}

func (service *CleanupService) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

func newCleanupPlanID() (string, error) {
	contents := make([]byte, 16)
	if _, err := rand.Read(contents); err != nil {
		return "", err
	}
	return "cleanup_plan_" + base64.RawURLEncoding.EncodeToString(contents), nil
}

func cleanupPlanContract(plan cleanupcontrol.Plan) contractv2.CleanupPlan {
	candidates := make([]contractv2.CleanupCandidate, len(plan.Candidates))
	for index, candidate := range plan.Candidates {
		candidates[index] = contractv2.CleanupCandidate{
			ID: candidate.ID, Kind: candidate.Kind, ProfileKey: candidate.ProfileKey,
			WorktreeID: candidate.WorktreeID, Fingerprint: candidate.Fingerprint,
			Bytes: candidate.Bytes, Path: candidate.Path,
		}
	}
	protections := make([]contractv2.CleanupProtection, len(plan.Protected))
	for index, protected := range plan.Protected {
		protections[index] = contractv2.CleanupProtection{
			Kind: protected.Kind, Path: protected.Path, Reason: protected.Reason,
			ProfileKey: protected.ProfileKey, WorktreeID: protected.WorktreeID,
		}
	}
	return contractv2.CleanupPlan{
		SchemaVersion: contractv2.SchemaVersion, ID: plan.ID, Revision: plan.Revision,
		Scope:      contractv2.CleanupScope{Kind: plan.Scope.Kind, ID: plan.Scope.ID},
		Candidates: candidates, Protected: protections, CreatedAt: plan.CreatedAt, ExpiresAt: plan.ExpiresAt,
	}
}

func cleanupResultContract(claim cleanupcontrol.Claim) (contractv2.CleanupResult, error) {
	result, err := claim.Result()
	if err != nil {
		return contractv2.CleanupResult{}, err
	}
	removals := make([]contractv2.CleanupRemoval, len(result.Removals))
	for index, removal := range result.Removals {
		removals[index] = contractv2.CleanupRemoval{CandidateID: removal.CandidateID, Removed: removal.Removed, Reason: removal.Reason}
	}
	return contractv2.CleanupResult{
		SchemaVersion: contractv2.SchemaVersion, PlanID: result.PlanID, PlanRevision: result.PlanRevision,
		Removals: removals, ClaimedAt: result.ClaimedAt, Attempts: result.Attempts, CompletedAt: result.CompletedAt,
	}, nil
}

var _ CleanupStore = (*state.Store)(nil)
