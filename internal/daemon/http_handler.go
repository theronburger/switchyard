package daemon

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/events"
)

type StatusSource interface {
	ReadSnapshot(ctx context.Context) (contractv1.StatusSnapshot, error)
}

type EventSource interface {
	ReadEvents(ctx context.Context, after events.Cursor, requestedLimit int) (events.Page, error)
}

type EnvironmentActions interface {
	StartEnvironment(context.Context, contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error)
	StopEnvironment(context.Context, string, contractv1.StopEnvironmentRequest) (contractv1.MutationReceipt, error)
}

type WorkspaceActions interface {
	CreateWorktree(context.Context, contractv1.CreateWorktreeRequest) (contractv1.MutationReceipt, error)
	AdoptWorktree(context.Context, contractv1.AdoptWorktreeRequest) (contractv1.MutationReceipt, error)
	ArchiveWorktree(context.Context, contractv1.ArchiveWorktreeRequest) (contractv1.MutationReceipt, error)
	PrepareWorktree(context.Context, contractv1.PrepareWorktreeRequest) (contractv1.MutationReceipt, error)
}

type OperationDiagnosticsSource interface {
	ReadOperationDiagnostics(context.Context, string, int) (contractv1.OperationDiagnostics, error)
}

type ConfigurationActions interface {
	Status(context.Context) (contractv1.ConfigurationStatus, error)
	Validate(context.Context, contractv1.ConfigurationValidationRequest) (contractv1.ConfigurationStatus, error)
	Accept(context.Context, contractv1.ConfigurationAcceptanceRequest) (contractv1.ConfigurationStatus, error)
}

type HandlerConfig struct {
	Token                string
	DaemonInstanceID     string
	DaemonVersion        string
	StartedAt            time.Time
	StatusSource         StatusSource
	EventSource          EventSource
	EnvironmentActions   EnvironmentActions
	WorkspaceActions     WorkspaceActions
	OperationDiagnostics OperationDiagnosticsSource
	Configuration        ConfigurationActions
}

type errorResponse struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Error         contractv1.ContractError `json:"error"`
}

func NewHTTPHandler(config HandlerConfig) (http.Handler, error) {
	if config.Token == "" {
		return nil, errors.New("daemon token is required")
	}
	if config.DaemonInstanceID == "" {
		return nil, errors.New("daemon instance id is required")
	}
	if config.DaemonVersion == "" {
		return nil, errors.New("daemon version is required")
	}
	if config.StartedAt.IsZero() {
		return nil, errors.New("daemon start time is required")
	}
	if config.StatusSource == nil {
		return nil, errors.New("status source is required")
	}

	eventSource := config.EventSource
	if eventSource == nil {
		eventSource, _ = config.StatusSource.(EventSource)
	}
	handler := &apiHandler{config: config, eventSource: eventSource}
	return http.HandlerFunc(handler.serveHTTP), nil
}

type apiHandler struct {
	config      HandlerConfig
	eventSource EventSource
}

func (handler *apiHandler) serveHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Content-Type", "application/json")

	if hasOrigin(request) {
		writeError(response, http.StatusForbidden, "HOSTILE_ORIGIN", "Browser-origin requests are not allowed", false)
		return
	}
	if routeRequiresAuthentication(request.URL.Path) && !tokenMatches(request, handler.config.Token) {
		writeError(response, http.StatusUnauthorized, "UNAUTHORIZED", "Authentication is required", false)
		return
	}

	switch request.URL.Path {
	case "/handshake":
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
			return
		}
		handler.handshake(response)
	case "/v1/status":
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
			return
		}
		handler.status(response, request)
	case "/v1/events":
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
			return
		}
		handler.events(response, request)
	default:
		if handler.configuration(response, request) {
			return
		}
		if handler.operationDiagnostics(response, request) {
			return
		}
		if handler.mutation(response, request) {
			return
		}
		writeError(response, http.StatusNotFound, "NOT_FOUND", "Route not found", false)
	}
}

func (handler *apiHandler) operationDiagnostics(response http.ResponseWriter, request *http.Request) bool {
	const prefix = "/v1/operations/"
	const suffix = "/diagnostics"
	if !strings.HasPrefix(request.URL.Path, prefix) || !strings.HasSuffix(request.URL.Path, suffix) {
		return false
	}
	operationID := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, prefix), suffix)
	if operationID == "" || strings.Contains(operationID, "/") {
		return false
	}
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", http.MethodGet)
		writeError(response, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed", false)
		return true
	}
	maximumBytes := 0
	query := request.URL.Query()
	if len(query) > 1 || (len(query) == 1 && !query.Has("maxBytes")) || len(query["maxBytes"]) > 1 {
		writeError(response, http.StatusBadRequest, "INVALID_DIAGNOSTICS_REQUEST", "Diagnostics request is invalid", false)
		return true
	}
	if value, present := query["maxBytes"]; present {
		if len(value) != 1 || value[0] == "" {
			writeError(response, http.StatusBadRequest, "INVALID_DIAGNOSTICS_REQUEST", "Diagnostics request is invalid", false)
			return true
		}
		parsed, err := strconv.Atoi(value[0])
		if err != nil {
			writeError(response, http.StatusBadRequest, "INVALID_DIAGNOSTICS_REQUEST", "Diagnostics request is invalid", false)
			return true
		}
		if parsed < 256 || parsed > MaximumOperationDiagnosticBytes {
			writeError(response, http.StatusBadRequest, "INVALID_DIAGNOSTICS_REQUEST", "Diagnostics request is invalid", false)
			return true
		}
		maximumBytes = parsed
	}
	if handler.config.OperationDiagnostics == nil {
		writeError(response, http.StatusServiceUnavailable, "DIAGNOSTICS_UNAVAILABLE", "Operation diagnostics are not available", true)
		return true
	}
	diagnostics, err := handler.config.OperationDiagnostics.ReadOperationDiagnostics(request.Context(), operationID, maximumBytes)
	switch {
	case errors.Is(err, ErrOperationDiagnosticsInvalid):
		writeError(response, http.StatusBadRequest, "INVALID_DIAGNOSTICS_REQUEST", "Diagnostics request is invalid", false)
	case errors.Is(err, ErrOperationDiagnosticsNotFound):
		writeError(response, http.StatusNotFound, "OPERATION_NOT_FOUND", "Operation was not found", false)
	case errors.Is(err, ErrOperationDiagnosticsUnavailable):
		writeError(response, http.StatusConflict, "DIAGNOSTICS_UNAVAILABLE", "This operation has no available diagnostics", false)
	case err != nil:
		writeError(response, http.StatusServiceUnavailable, "DIAGNOSTICS_UNAVAILABLE", "Operation diagnostics are not available", true)
	default:
		writeJSON(response, http.StatusOK, diagnostics)
	}
	return true
}

func (handler *apiHandler) handshake(response http.ResponseWriter) {
	writeJSON(response, http.StatusOK, contractv1.Handshake{
		SchemaVersion:           contractv1.SchemaVersion,
		DaemonInstanceID:        handler.config.DaemonInstanceID,
		DaemonVersion:           handler.config.DaemonVersion,
		SupportedSchemaVersions: []int{contractv1.SchemaVersion},
	})
}

func (handler *apiHandler) status(response http.ResponseWriter, request *http.Request) {
	snapshot, err := handler.config.StatusSource.ReadSnapshot(request.Context())
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "STATUS_UNAVAILABLE", "Status is not ready", true)
		return
	}
	if snapshot.SchemaVersion != contractv1.SchemaVersion ||
		snapshot.Daemon.InstanceID != handler.config.DaemonInstanceID ||
		snapshot.Daemon.Version != handler.config.DaemonVersion {
		writeError(response, http.StatusServiceUnavailable, "STATUS_UNAVAILABLE", "Status belongs to an incompatible daemon instance", true)
		return
	}
	if err := snapshot.Validate(); err != nil {
		writeError(response, http.StatusInternalServerError, "INVALID_STATUS", "Stored status is invalid", false)
		return
	}
	writeJSON(response, http.StatusOK, snapshot)
}

func hasOrigin(request *http.Request) bool {
	for _, origin := range request.Header.Values("Origin") {
		if origin != "" {
			return true
		}
	}
	return false
}

func routeRequiresAuthentication(path string) bool {
	return path == "/handshake" || path == "/v1" || strings.HasPrefix(path, "/v1/")
}

func tokenMatches(request *http.Request, expectedToken string) bool {
	values := request.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	providedHash := sha256.Sum256([]byte(strings.TrimPrefix(values[0], "Bearer ")))
	expectedHash := sha256.Sum256([]byte(expectedToken))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}

func writeError(response http.ResponseWriter, status int, code, message string, retryable bool) {
	writeContractError(response, status, contractv1.ContractError{Code: code, Message: message, Retryable: retryable})
}

func writeContractError(response http.ResponseWriter, status int, contractError contractv1.ContractError) {
	writeJSON(response, status, errorResponse{
		SchemaVersion: contractv1.SchemaVersion,
		Error:         contractError,
	})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
