package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

// OccupancyActions records and ends explicit worktree handoff leases.
type OccupancyActions interface {
	AcquireOccupancy(context.Context, contractv2.AcquireOccupancyRequest) (contractv2.OccupancyLease, error)
	ReleaseOccupancy(context.Context, contractv2.ReleaseOccupancyRequest) (contractv2.OccupancyLease, error)
}

// OccupancyStore is the durable lease writer. The daemon's state store is the
// only implementation in production.
type OccupancyStore interface {
	AcquireOccupancy(context.Context, state.NewOccupancyLease) (contractv2.OccupancyLease, bool, error)
	ReleaseOccupancy(ctx context.Context, worktreeID, leaseID string) (contractv2.OccupancyLease, error)
}

// OccupancyService is the daemon-owned occupancy lease writer. A lease is a
// conservative record that an owner-launched task was handed a worktree: it
// is never inferred from a deep link, a process, or a transcript, and it ends
// only when a client releases it (D-009a, PRIVATE_REPOSITORY_PROFILES
// "Agent integration").
type OccupancyService struct {
	Store OccupancyStore
	NewID func(string) (string, error)
}

func (service *OccupancyService) AcquireOccupancy(
	ctx context.Context,
	request contractv2.AcquireOccupancyRequest,
) (contractv2.OccupancyLease, error) {
	if service == nil || service.Store == nil {
		return contractv2.OccupancyLease{}, occupancyUnavailable()
	}
	if request.Validate() != nil {
		return contractv2.OccupancyLease{}, invalidOccupancyRequest()
	}
	newID := service.NewID
	if newID == nil {
		newID = randomActionID
	}
	leaseID, err := newID("occupancy")
	if err != nil {
		return contractv2.OccupancyLease{}, occupancyUnavailable()
	}
	lease, _, err := service.Store.AcquireOccupancy(ctx, state.NewOccupancyLease{
		ID: leaseID, RequestID: request.RequestID, WorktreeID: request.WorktreeID,
		HolderKind: request.HolderKind, HolderLabel: request.HolderLabel,
	})
	switch {
	case errors.Is(err, state.ErrOccupancyWorktreeUnknown):
		return contractv2.OccupancyLease{}, &ActionError{Status: http.StatusNotFound, Contract: contractv2.ContractError{
			Code: "WORKTREE_NOT_FOUND", Message: "The requested worktree is not available.",
			ResourceKind: "worktree", ResourceID: request.WorktreeID,
		}}
	case errors.Is(err, state.ErrOccupancyLimit):
		return contractv2.OccupancyLease{}, &ActionError{Status: http.StatusConflict, Contract: contractv2.ContractError{
			Code: "OCCUPANCY_LIMIT", Message: "This worktree already holds the maximum number of handoff leases. Release one before handing it off again.",
			ResourceKind: "worktree", ResourceID: request.WorktreeID, NextAction: "release_occupancy",
		}}
	case errors.Is(err, state.ErrOccupancyRequestReused):
		return contractv2.OccupancyLease{}, &ActionError{Status: http.StatusConflict, Contract: contractv2.ContractError{
			Code: "OCCUPANCY_REQUEST_CONFLICT", Message: "This request ID was already used for a different handoff lease.",
			ResourceKind: "worktree", ResourceID: request.WorktreeID,
		}}
	case errors.Is(err, state.ErrNoSnapshot):
		return contractv2.OccupancyLease{}, occupancyUnavailable()
	case err != nil:
		return contractv2.OccupancyLease{}, occupancyUnavailable()
	}
	return lease, nil
}

func (service *OccupancyService) ReleaseOccupancy(
	ctx context.Context,
	request contractv2.ReleaseOccupancyRequest,
) (contractv2.OccupancyLease, error) {
	if service == nil || service.Store == nil {
		return contractv2.OccupancyLease{}, occupancyUnavailable()
	}
	if request.Validate() != nil {
		return contractv2.OccupancyLease{}, invalidOccupancyRequest()
	}
	lease, err := service.Store.ReleaseOccupancy(ctx, request.WorktreeID, request.LeaseID)
	switch {
	case errors.Is(err, state.ErrOccupancyLeaseNotFound):
		return contractv2.OccupancyLease{}, &ActionError{Status: http.StatusNotFound, Contract: contractv2.ContractError{
			Code: "OCCUPANCY_LEASE_NOT_FOUND", Message: "No handoff lease with this ID belongs to the worktree.",
			ResourceKind: "worktree", ResourceID: request.WorktreeID,
		}}
	case err != nil:
		return contractv2.OccupancyLease{}, occupancyUnavailable()
	}
	return lease, nil
}

func invalidOccupancyRequest() error {
	return &ActionError{Status: http.StatusBadRequest, Contract: contractv2.ContractError{
		Code: "INVALID_OCCUPANCY_REQUEST", Message: "The occupancy request is invalid.",
	}}
}

func occupancyUnavailable() error {
	return &ActionError{Status: http.StatusServiceUnavailable, Contract: contractv2.ContractError{
		Code: "OCCUPANCY_UNAVAILABLE", Message: "Worktree occupancy is temporarily unavailable.", Retryable: true,
	}}
}

// occupancy routes:
//
//	POST /v1/worktrees/{worktreeId}/occupancy                         acquire
//	POST /v1/worktrees/{worktreeId}/occupancy/{leaseId}/release      release
func (handler *apiHandler) occupancy(response http.ResponseWriter, request *http.Request) bool {
	const prefix = "/v1/worktrees/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, prefix), "/")
	switch {
	case len(parts) == 2 && parts[1] == "occupancy" && safePathIdentifier(parts[0]):
		handler.acquireOccupancy(response, request, parts[0])
		return true
	case len(parts) == 4 && parts[1] == "occupancy" && parts[3] == "release" &&
		safePathIdentifier(parts[0]) && safePathIdentifier(parts[2]):
		handler.releaseOccupancy(response, request, parts[0], parts[2])
		return true
	default:
		return false
	}
}

func (handler *apiHandler) acquireOccupancy(response http.ResponseWriter, request *http.Request, worktreeID string) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
		return
	}
	var acquire contractv2.AcquireOccupancyRequest
	if err := decodeMutationRequest(request, &acquire); err != nil || acquire.Validate() != nil || acquire.WorktreeID != worktreeID {
		writeDecodeFailure(response, err, "INVALID_OCCUPANCY_REQUEST", "The occupancy request is invalid")
		return
	}
	if handler.config.Occupancy == nil {
		writeError(response, http.StatusServiceUnavailable, "OCCUPANCY_UNAVAILABLE", "Worktree occupancy is unavailable", true)
		return
	}
	lease, err := handler.config.Occupancy.AcquireOccupancy(request.Context(), acquire)
	handler.writeOccupancyResult(response, lease, err)
}

func (handler *apiHandler) releaseOccupancy(response http.ResponseWriter, request *http.Request, worktreeID, leaseID string) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
		return
	}
	var release contractv2.ReleaseOccupancyRequest
	if err := decodeMutationRequest(request, &release); err != nil || release.Validate() != nil ||
		release.WorktreeID != worktreeID || release.LeaseID != leaseID {
		writeDecodeFailure(response, err, "INVALID_OCCUPANCY_REQUEST", "The occupancy request is invalid")
		return
	}
	if handler.config.Occupancy == nil {
		writeError(response, http.StatusServiceUnavailable, "OCCUPANCY_UNAVAILABLE", "Worktree occupancy is unavailable", true)
		return
	}
	lease, err := handler.config.Occupancy.ReleaseOccupancy(request.Context(), release)
	handler.writeOccupancyResult(response, lease, err)
}

func (handler *apiHandler) writeOccupancyResult(response http.ResponseWriter, lease contractv2.OccupancyLease, err error) {
	if err != nil {
		var actionError *ActionError
		if errors.As(err, &actionError) && validActionError(actionError) {
			writeContractError(response, actionError.Status, actionError.Contract)
			return
		}
		writeError(response, http.StatusServiceUnavailable, "OCCUPANCY_UNAVAILABLE", "Worktree occupancy is unavailable", true)
		return
	}
	if lease.Validate() != nil {
		writeError(response, http.StatusInternalServerError, "INVALID_OCCUPANCY_LEASE", "The daemon produced an invalid occupancy lease", false)
		return
	}
	writeJSON(response, http.StatusOK, lease)
}
