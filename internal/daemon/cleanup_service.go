package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	cleanupcontrol "github.com/theronburger/switchyard/internal/control/cleanup"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/state"
)

const cleanupPlanLifetime = 10 * time.Minute

type CleanupStore interface {
	SaveCleanupPlan(context.Context, cleanupcontrol.Plan) (cleanupcontrol.Plan, error)
	ReadCleanupPlan(context.Context, string, int64) (cleanupcontrol.Plan, error)
	ConsumeCleanupPlan(context.Context, string, int64) error
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
}

func (service CleanupService) Plan(ctx context.Context, request contractv1.CleanupPlanRequest) (contractv1.CleanupPlan, error) {
	if request.Validate() != nil || service.Store == nil || service.Workspaces == nil || service.RuntimeRoot == "" {
		return contractv1.CleanupPlan{}, errors.New("cleanup service is unavailable")
	}
	planner, err := service.planner(ctx)
	if err != nil {
		return contractv1.CleanupPlan{}, err
	}
	inventory, err := planner.Inventory(ctx, cleanupcontrol.Scope{Kind: request.Scope.Kind, ID: request.Scope.ID})
	if err != nil {
		return contractv1.CleanupPlan{}, err
	}
	newID := service.NewID
	if newID == nil {
		newID = newCleanupPlanID
	}
	id, err := newID()
	if err != nil {
		return contractv1.CleanupPlan{}, err
	}
	now := service.now()
	plan, err := service.Store.SaveCleanupPlan(ctx, cleanupcontrol.Plan{
		SchemaVersion: 1, ID: id, Scope: cleanupcontrol.Scope{Kind: request.Scope.Kind, ID: request.Scope.ID},
		Candidates: inventory.Candidates, Protected: inventory.Protected,
		CreatedAt: now, ExpiresAt: now.Add(cleanupPlanLifetime),
	})
	if err != nil {
		return contractv1.CleanupPlan{}, err
	}
	return cleanupPlanContract(plan), nil
}

func (service CleanupService) Apply(ctx context.Context, request contractv1.CleanupApplyRequest) (contractv1.CleanupResult, error) {
	if request.Validate() != nil || service.Store == nil || service.Workspaces == nil {
		return contractv1.CleanupResult{}, errors.New("cleanup service is unavailable")
	}
	plan, err := service.Store.ReadCleanupPlan(ctx, request.PlanID, request.ExpectedRevision)
	if err != nil {
		return contractv1.CleanupResult{}, err
	}
	planner, err := service.planner(ctx)
	if err != nil {
		return contractv1.CleanupResult{}, err
	}
	byID := make(map[string]cleanupcontrol.Candidate, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		byID[candidate.ID] = candidate
	}
	removals := make([]contractv1.CleanupRemoval, 0, len(request.CandidateIDs))
	for _, id := range request.CandidateIDs {
		candidate, found := byID[id]
		if !found {
			removals = append(removals, contractv1.CleanupRemoval{CandidateID: id, Reason: "not-in-plan"})
			continue
		}
		if err := planner.Remove(ctx, candidate); err != nil {
			removals = append(removals, contractv1.CleanupRemoval{CandidateID: id, Reason: "changed-or-protected"})
			continue
		}
		removals = append(removals, contractv1.CleanupRemoval{CandidateID: id, Removed: true})
	}
	if err := service.Store.ConsumeCleanupPlan(ctx, plan.ID, plan.Revision); err != nil {
		return contractv1.CleanupResult{}, err
	}
	return contractv1.CleanupResult{
		SchemaVersion: contractv1.SchemaVersion, PlanID: plan.ID, PlanRevision: plan.Revision,
		Removals: removals, CompletedAt: service.now(),
	}, nil
}

func (service CleanupService) planner(ctx context.Context) (cleanupcontrol.PrivatePreparationPlanner, error) {
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

func (service CleanupService) now() time.Time {
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

func cleanupPlanContract(plan cleanupcontrol.Plan) contractv1.CleanupPlan {
	candidates := make([]contractv1.CleanupCandidate, len(plan.Candidates))
	for index, candidate := range plan.Candidates {
		candidates[index] = contractv1.CleanupCandidate{
			ID: candidate.ID, Kind: candidate.Kind, ProfileKey: candidate.ProfileKey,
			WorktreeID: candidate.WorktreeID, Fingerprint: candidate.Fingerprint,
			Bytes: candidate.Bytes, Path: candidate.Path,
		}
	}
	protections := make([]contractv1.CleanupProtection, len(plan.Protected))
	for index, protected := range plan.Protected {
		protections[index] = contractv1.CleanupProtection{
			Kind: protected.Kind, Path: protected.Path, Reason: protected.Reason,
			ProfileKey: protected.ProfileKey, WorktreeID: protected.WorktreeID,
		}
	}
	return contractv1.CleanupPlan{
		SchemaVersion: contractv1.SchemaVersion, ID: plan.ID, Revision: plan.Revision,
		Scope:      contractv1.CleanupScope{Kind: plan.Scope.Kind, ID: plan.Scope.ID},
		Candidates: candidates, Protected: protections, CreatedAt: plan.CreatedAt, ExpiresAt: plan.ExpiresAt,
	}
}

var _ CleanupStore = (*state.Store)(nil)
