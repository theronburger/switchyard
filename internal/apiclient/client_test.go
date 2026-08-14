package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

func TestClientAuthenticatesHandshakeAndStatusWithoutOrigin(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("authorization: got %q", got)
		}
		if origin := request.Header.Get("Origin"); origin != "" {
			t.Errorf("native client sent Origin %q", origin)
		}
		secureJSONHeaders(response)
		switch request.URL.Path {
		case "/handshake":
			writeTestJSON(t, response, Handshake{
				SchemaVersion:           RuntimeDescriptorSchemaVersion,
				DaemonInstanceID:        snapshot.Daemon.InstanceID,
				DaemonVersion:           snapshot.Daemon.Version,
				SupportedSchemaVersions: []int{contractv1.SchemaVersion},
			})
		case "/v1/status":
			writeTestJSON(t, response, snapshot)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	connection := connectionForServer(t, server.URL, token, snapshot, now)
	client := NewClient(connection, ClientOptions{})
	got, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.SnapshotRevision != snapshot.SnapshotRevision {
		t.Fatalf("revision: got %d, want %d", got.SnapshotRevision, snapshot.SnapshotRevision)
	}
}

func TestClientRejectsUnknownDaemonBeforeStatus(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	var statusRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		if request.URL.Path == "/handshake" {
			writeTestJSON(t, response, Handshake{
				SchemaVersion:           RuntimeDescriptorSchemaVersion,
				DaemonInstanceID:        "daemon_foreign",
				DaemonVersion:           snapshot.Daemon.Version,
				SupportedSchemaVersions: []int{contractv1.SchemaVersion},
			})
			return
		}
		statusRequests.Add(1)
		writeTestJSON(t, response, snapshot)
	}))
	defer server.Close()

	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	_, err := client.Status(context.Background())
	if CodeOf(err) != ErrorDaemonUnknown {
		t.Fatalf("error code: got %q, want %q", CodeOf(err), ErrorDaemonUnknown)
	}
	if statusRequests.Load() != 0 {
		t.Fatal("status was requested before daemon identity was established")
	}
}

func TestClientDoesNotFollowRedirectsOrLeakToken(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	var hostileRequests atomic.Int32
	hostile := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		hostileRequests.Add(1)
		if strings.Contains(request.Header.Get("Authorization"), token) {
			t.Error("redirect leaked bearer token")
		}
	}))
	defer hostile.Close()
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, hostile.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client := NewClient(connectionForServer(t, source.URL, token, snapshot, now), ClientOptions{})
	_, err := client.Handshake(context.Background())
	if CodeOf(err) != ErrorDaemonResponseInvalid {
		t.Fatalf("error code: got %q", CodeOf(err))
	}
	if hostileRequests.Load() != 0 {
		t.Fatalf("hostile server received %d request(s)", hostileRequests.Load())
	}
}

func TestClientRejectsMissingSecurityHeaders(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		writeTestJSON(t, response, Handshake{
			SchemaVersion:           RuntimeDescriptorSchemaVersion,
			DaemonInstanceID:        snapshot.Daemon.InstanceID,
			DaemonVersion:           snapshot.Daemon.Version,
			SupportedSchemaVersions: []int{contractv1.SchemaVersion},
		})
	}))
	defer server.Close()

	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	_, err := client.Handshake(context.Background())
	if CodeOf(err) != ErrorDaemonResponseInvalid {
		t.Fatalf("error code: got %q, want %q", CodeOf(err), ErrorDaemonResponseInvalid)
	}
}

func TestClientNeverIncludesResponseBodyInErrors(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	token := testToken()
	secretBody := "response-secret-must-not-appear"
	snapshot := validSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
		_, _ = response.Write([]byte(secretBody))
	}))
	defer server.Close()

	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	_, err := client.Handshake(context.Background())
	if err == nil {
		t.Fatal("expected handshake failure")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), secretBody) {
		t.Fatal("client error leaked a token or response body")
	}
}

func connectionForServer(
	t *testing.T,
	serverURL string,
	token string,
	snapshot contractv1.StatusSnapshot,
	now time.Time,
) Connection {
	t.Helper()
	paths := writeRuntimeFiles(t, RuntimeDescriptor{
		SchemaVersion:    RuntimeDescriptorSchemaVersion,
		Endpoint:         serverURL,
		DaemonInstanceID: snapshot.Daemon.InstanceID,
		DaemonVersion:    snapshot.Daemon.Version,
		PID:              123,
		ProcessStartedAt: now.Add(-time.Hour),
		GeneratedAt:      now,
	}, token)
	connection, err := Discover(paths, DiscoveryPolicy{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func validSnapshot(now time.Time) contractv1.StatusSnapshot {
	return contractv1.StatusSnapshot{
		SchemaVersion:    contractv1.SchemaVersion,
		SnapshotRevision: 42,
		GeneratedAt:      now,
		Daemon: contractv1.DaemonStatus{
			InstanceID: "daemon_test",
			Version:    "0.1.0-dev",
			State:      "ready",
			StartedAt:  now.Add(-time.Hour),
		},
		Repositories: []contractv1.Repository{},
		Environments: []contractv1.Environment{},
		Operations:   []contractv1.Operation{},
		Alerts:       []contractv1.Alert{},
	}
}

func secureJSONHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeTestJSON(t *testing.T, response http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func ExampleCodedError_Error() {
	err := newCodedError(ErrorDaemonUnauthorized, fmt.Errorf("secret detail"))
	fmt.Println(err)
	// Output: DAEMON_UNAUTHORIZED
}
