package daemon

import (
	"errors"
	"net/http"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

func (handler *apiHandler) configuration(response http.ResponseWriter, request *http.Request) bool {
	if request.URL.Path != "/v1/configuration" && request.URL.Path != "/v1/configuration/validate" &&
		request.URL.Path != "/v1/configuration/accept" && request.URL.Path != "/v1/configuration/repositories" {
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
		var validation contractv2.ConfigurationValidationRequest
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
		var acceptance contractv2.ConfigurationAcceptanceRequest
		if decodeMutationRequest(request, &acceptance) != nil || acceptance.Validate() != nil {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "Configuration acceptance request is invalid", false)
			return true
		}
		status, err := handler.config.Configuration.Accept(request.Context(), acceptance)
		handler.writeConfigurationResult(response, status, err)
	case "/v1/configuration/repositories":
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
			return true
		}
		var mutation contractv2.ConfigurationRepositoryMutationRequest
		if decodeMutationRequest(request, &mutation) != nil || mutation.Validate() != nil {
			writeError(response, http.StatusBadRequest, "INVALID_REQUEST", "Configuration repository mutation is invalid", false)
			return true
		}
		status, err := handler.config.Configuration.MutateRepository(request.Context(), mutation)
		handler.writeConfigurationResult(response, status, err)
	}
	return true
}

func (handler *apiHandler) writeConfigurationResult(
	response http.ResponseWriter,
	status contractv2.ConfigurationStatus,
	err error,
) {
	if err != nil {
		var rejection ConfigurationRejectedError
		switch {
		case errors.Is(err, state.ErrConfigurationRevisionConflict):
			writeError(response, http.StatusConflict, "CONFIGURATION_REVISION_CONFLICT", "Configuration changed before this request completed", true)
		case errors.Is(err, state.ErrConfigurationCandidateMissing):
			writeError(response, http.StatusConflict, "CONFIGURATION_NOT_STAGED", "Configuration must be validated before acceptance", false)
		case errors.Is(err, ErrConfigurationDesiredChanged):
			writeError(response, http.StatusConflict, "CONFIGURATION_DESIRED_CHANGED", "configuration.yaml changed since it was last read; reload and retry", true)
		case errors.Is(err, configuration.ErrRepositoryRootBound):
			writeError(response, http.StatusConflict, "CONFIGURATION_ROOT_BOUND", "A repository key is permanently bound to its root; remove the entry and add a new one", false)
		case errors.Is(err, configuration.ErrRepositoryMissing):
			writeError(response, http.StatusNotFound, "CONFIGURATION_REPOSITORY_MISSING", "configuration.yaml has no entry with that key", false)
		case errors.Is(err, ErrConfigurationRepositoryEnabled):
			writeError(response, http.StatusConflict, "CONFIGURATION_REPOSITORY_ENABLED", "Disable the repository and accept that revision before removing it", false)
		case errors.Is(err, ErrConfigurationRepositoryReferenced):
			writeError(response, http.StatusConflict, "CONFIGURATION_REPOSITORY_REFERENCED", "Stop its environments and archive its managed worktrees before removing the repository", false)
		case errors.As(err, &rejection):
			writeError(response, http.StatusBadRequest, "CONFIGURATION_INVALID", "Private configuration is invalid: "+rejection.Reason, false)
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
