package daemon

import (
	"errors"
	"net/http"
	"strings"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

const cleanupApplySuffix = "/apply"

func (handler *apiHandler) cleanup(response http.ResponseWriter, request *http.Request) bool {
	if request.URL.Path == "/v1/cleanup/plans" {
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
			return true
		}
		if handler.config.Cleanup == nil {
			writeError(response, http.StatusServiceUnavailable, "CLEANUP_UNAVAILABLE", "Cleanup is unavailable", true)
			return true
		}
		var cleanupRequest contractv2.CleanupPlanRequest
		if decodeMutationRequest(request, &cleanupRequest) != nil || cleanupRequest.Validate() != nil {
			writeError(response, http.StatusBadRequest, "INVALID_CLEANUP_REQUEST", "Cleanup plan request is invalid", false)
			return true
		}
		plan, err := handler.config.Cleanup.Plan(request.Context(), cleanupRequest)
		if err != nil {
			writeError(response, http.StatusServiceUnavailable, "CLEANUP_UNAVAILABLE", "Cleanup inventory is unavailable", true)
			return true
		}
		if plan.Validate() != nil {
			writeError(response, http.StatusInternalServerError, "INVALID_CLEANUP_PLAN", "Cleanup returned an invalid plan", false)
			return true
		}
		writeJSON(response, http.StatusCreated, plan)
		return true
	}

	planID, matched := cleanupApplyPath(request.URL.Path)
	if !matched {
		return false
	}
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
		return true
	}
	if handler.config.Cleanup == nil {
		writeError(response, http.StatusServiceUnavailable, "CLEANUP_UNAVAILABLE", "Cleanup is unavailable", true)
		return true
	}
	var applyRequest contractv2.CleanupApplyRequest
	if decodeMutationRequest(request, &applyRequest) != nil || applyRequest.Validate() != nil || applyRequest.PlanID != planID {
		writeError(response, http.StatusBadRequest, "INVALID_CLEANUP_REQUEST", "Cleanup apply request is invalid", false)
		return true
	}
	result, err := handler.config.Cleanup.Apply(request.Context(), applyRequest)
	switch {
	case errors.Is(err, state.ErrCleanupPlanNotFound):
		writeError(response, http.StatusConflict, "CLEANUP_PLAN_CHANGED", "Cleanup plan changed before it was applied", true)
	case errors.Is(err, state.ErrCleanupPlanExpired):
		writeError(response, http.StatusConflict, "CLEANUP_PLAN_EXPIRED", "Cleanup plan expired; review a new plan", true)
	case errors.Is(err, state.ErrCleanupPlanConsumed):
		writeError(response, http.StatusConflict, "CLEANUP_PLAN_CONSUMED", "Cleanup plan was already applied", false)
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "CLEANUP_UNAVAILABLE", "Cleanup could not be applied", true)
	case result.Validate() != nil:
		writeError(response, http.StatusInternalServerError, "INVALID_CLEANUP_RESULT", "Cleanup returned an invalid result", false)
	default:
		writeJSON(response, http.StatusOK, result)
	}
	return true
}

func cleanupApplyPath(path string) (string, bool) {
	const prefix = "/v1/cleanup/plans/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, cleanupApplySuffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), cleanupApplySuffix)
	return id, id != "" && !strings.Contains(id, "/")
}
