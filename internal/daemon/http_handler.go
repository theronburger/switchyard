package daemon

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
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

type HandlerConfig struct {
	Token            string
	DaemonInstanceID string
	DaemonVersion    string
	StartedAt        time.Time
	StatusSource     StatusSource
	EventSource      EventSource
}

type errorResponse struct {
	SchemaVersion int              `json:"schemaVersion"`
	Error         errorDescription `json:"error"`
}

type errorDescription struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
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
		writeError(response, http.StatusNotFound, "NOT_FOUND", "Route not found", false)
	}
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
	writeJSON(response, status, errorResponse{
		SchemaVersion: contractv1.SchemaVersion,
		Error: errorDescription{
			Code:      code,
			Message:   message,
			Retryable: retryable,
		},
	})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
