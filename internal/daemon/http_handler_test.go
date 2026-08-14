package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

const testToken = "test-token-that-never-appears-in-responses"

type staticStatusSource struct {
	snapshot contractv1.StatusSnapshot
	err      error
}

func (source staticStatusSource) ReadSnapshot(context.Context) (contractv1.StatusSnapshot, error) {
	return source.snapshot, source.err
}

func TestHTTPHandlerRequiresAuthenticationAndSecurityHeaders(t *testing.T) {
	handler := newTestHandler(t, validHTTPStatus())
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got, want := response.Code, http.StatusUnauthorized; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
	if got, want := response.Header().Get("Cache-Control"), "no-store"; got != want {
		t.Fatalf("cache control: got %q, want %q", got, want)
	}
	if got, want := response.Header().Get("X-Content-Type-Options"), "nosniff"; got != want {
		t.Fatalf("content type options: got %q, want %q", got, want)
	}
	if strings.Contains(response.Body.String(), testToken) {
		t.Fatal("authentication response disclosed the token")
	}
}

func TestHTTPHandlerRejectsEveryNonemptyOrigin(t *testing.T) {
	handler := newTestHandler(t, validHTTPStatus())
	request := authenticatedRequest(http.MethodGet, "/v1/status")
	request.Header.Set("Origin", "null")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got, want := response.Code, http.StatusForbidden; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
}

func TestHandshakeReturnsExactSupportedVersion(t *testing.T) {
	handler := newTestHandler(t, validHTTPStatus())
	request := authenticatedRequest(http.MethodGet, "/handshake")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got, want := response.Code, http.StatusOK; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
	var handshake contractv1.Handshake
	if err := json.Unmarshal(response.Body.Bytes(), &handshake); err != nil {
		t.Fatal(err)
	}
	if got, want := handshake.DaemonInstanceID, "daemon_01"; got != want {
		t.Fatalf("daemon instance: got %q, want %q", got, want)
	}
	if len(handshake.SupportedSchemaVersions) != 1 || handshake.SupportedSchemaVersions[0] != contractv1.SchemaVersion {
		t.Fatalf("supported versions: got %v", handshake.SupportedSchemaVersions)
	}
}

func TestStatusReturnsAtomicContractSnapshot(t *testing.T) {
	want := validHTTPStatus()
	handler := newTestHandler(t, want)
	request := authenticatedRequest(http.MethodGet, "/v1/status")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got, expectedStatus := response.Code, http.StatusOK; got != expectedStatus {
		t.Fatalf("status: got %d, want %d", got, expectedStatus)
	}
	var got contractv1.StatusSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SnapshotRevision != want.SnapshotRevision || got.Daemon.InstanceID != want.Daemon.InstanceID {
		t.Fatalf("snapshot changed: got %+v, want %+v", got, want)
	}
}

func TestStatusRejectsSnapshotFromPreviousDaemon(t *testing.T) {
	snapshot := validHTTPStatus()
	snapshot.Daemon.InstanceID = "daemon_previous"
	handler := newTestHandler(t, snapshot)
	request := authenticatedRequest(http.MethodGet, "/v1/status")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got, want := response.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
}

func TestStatusHidesStorageFailure(t *testing.T) {
	handler, err := NewHTTPHandler(HandlerConfig{
		Token:            testToken,
		DaemonInstanceID: "daemon_01",
		DaemonVersion:    "0.1.0-dev",
		StartedAt:        time.Now(),
		StatusSource:     staticStatusSource{err: errors.New("private database path and details")},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedRequest(http.MethodGet, "/v1/status")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if got, want := response.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("status: got %d, want %d", got, want)
	}
	if strings.Contains(response.Body.String(), "database") || strings.Contains(response.Body.String(), "path") {
		t.Fatalf("storage failure leaked details: %s", response.Body.String())
	}
}

func newTestHandler(t *testing.T, snapshot contractv1.StatusSnapshot) http.Handler {
	t.Helper()
	handler, err := NewHTTPHandler(HandlerConfig{
		Token:            testToken,
		DaemonInstanceID: "daemon_01",
		DaemonVersion:    "0.1.0-dev",
		StartedAt:        time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC),
		StatusSource:     staticStatusSource{snapshot: snapshot},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func authenticatedRequest(method, path string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+testToken)
	return request
}

func validHTTPStatus() contractv1.StatusSnapshot {
	return contractv1.StatusSnapshot{
		SchemaVersion:    contractv1.SchemaVersion,
		SnapshotRevision: 1,
		GeneratedAt:      time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC),
		Daemon: contractv1.DaemonStatus{
			InstanceID: "daemon_01",
			Version:    "0.1.0-dev",
			State:      "ready",
			StartedAt:  time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC),
		},
		Repositories: []contractv1.Repository{},
		Environments: []contractv1.Environment{},
		Operations:   []contractv1.Operation{},
		Alerts:       []contractv1.Alert{},
	}
}
