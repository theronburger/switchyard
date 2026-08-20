package daemon

import (
	"errors"
	"net/http"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/state"
)

func (handler *apiHandler) configuration(response http.ResponseWriter, request *http.Request) bool {
	if request.URL.Path != "/v1/configuration" && request.URL.Path != "/v1/configuration/validate" &&
		request.URL.Path != "/v1/configuration/accept" {
		return false
	}
	if handler.config.Configuration == nil {
		writeError(response, http.StatusServiceUnavailable, "CONFIGURATION_UNAVAILABLE", "Configuration is unavailable", true)
		return true
	}
	switch request.URL.Path {
	case "/v1/configuration":
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
			return true
		}
		status, err := handler.config.Configuration.Status(request.Context())
		handler.writeConfigurationResult(response, status, err)
	case "/v1/configuration/validate":
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
			return true
		}
		var validation contractv1.ConfigurationValidationRequest
		if decodeMutationRequest(request, &validation) != nil || validation.Validate() != nil {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "Configuration validation request is invalid", false)
			return true
		}
		status, err := handler.config.Configuration.Validate(request.Context(), validation)
		handler.writeConfigurationResult(response, status, err)
	case "/v1/configuration/accept":
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
			return true
		}
		var acceptance contractv1.ConfigurationAcceptanceRequest
		if decodeMutationRequest(request, &acceptance) != nil || acceptance.Validate() != nil {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "Configuration acceptance request is invalid", false)
			return true
		}
		status, err := handler.config.Configuration.Accept(request.Context(), acceptance)
		handler.writeConfigurationResult(response, status, err)
	}
	return true
}

func (handler *apiHandler) writeConfigurationResult(
	response http.ResponseWriter,
	status contractv1.ConfigurationStatus,
	err error,
) {
	if err != nil {
		switch {
		case errors.Is(err, state.ErrConfigurationRevisionConflict):
			writeError(response, http.StatusConflict, "CONFIGURATION_REVISION_CONFLICT", "Configuration changed before this request completed", true)
		case errors.Is(err, state.ErrConfigurationCandidateMissing):
			writeError(response, http.StatusConflict, "CONFIGURATION_NOT_STAGED", "Configuration must be validated before acceptance", false)
		default:
			writeError(response, http.StatusBadRequest, "CONFIGURATION_INVALID", "Private configuration is invalid", false)
		}
		return
	}
	if status.Validate() != nil {
		writeError(response, http.StatusInternalServerError, "CONFIGURATION_STATUS_INVALID", "Configuration status is invalid", false)
		return
	}
	writeJSON(response, http.StatusOK, status)
}
