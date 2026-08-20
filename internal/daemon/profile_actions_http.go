package daemon

import (
	"context"
	"net/http"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

type ProfileActions interface {
	ListActions(context.Context) (contractv2.ProfileActionList, error)
	RunAction(context.Context, contractv2.RunProfileActionRequest) (contractv2.MutationReceipt, error)
}

func (handler *apiHandler) profileActions(response http.ResponseWriter, request *http.Request) bool {
	switch request.URL.Path {
	case "/v1/actions":
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
			return true
		}
		if len(request.URL.Query()) != 0 {
			writeError(response, http.StatusBadRequest, "INVALID_ACTION_REQUEST", "The action list request is invalid", false)
			return true
		}
		if handler.config.ProfileActions == nil {
			writeError(response, http.StatusServiceUnavailable, "ACTIONS_UNAVAILABLE", "Profile actions are unavailable", true)
			return true
		}
		list, err := handler.config.ProfileActions.ListActions(request.Context())
		if err != nil {
			handler.writeMutationResult(response, contractv2.MutationReceipt{}, err)
			return true
		}
		if list.Validate() != nil {
			writeError(response, http.StatusInternalServerError, "INVALID_ACTION_LIST", "Profile actions returned an invalid list", false)
			return true
		}
		writeJSON(response, http.StatusOK, list)
		return true
	case "/v1/actions/run":
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
			return true
		}
		var mutation contractv2.RunProfileActionRequest
		if err := decodeMutationRequest(request, &mutation); err != nil || mutation.Validate() != nil {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "The profile action request is invalid", false)
			return true
		}
		if handler.config.ProfileActions == nil {
			writeError(response, http.StatusServiceUnavailable, "ACTIONS_UNAVAILABLE", "Profile actions are unavailable", true)
			return true
		}
		receipt, err := handler.config.ProfileActions.RunAction(request.Context(), mutation)
		handler.writeMutationResult(response, receipt, err)
		return true
	default:
		return false
	}
}
