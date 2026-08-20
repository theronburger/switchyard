package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

func TestClientDeclaresExactSchemaVersionOnEveryRequest(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	declared := make(map[string]string)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		declared[request.URL.Path] = request.Header.Get(contractv2.SchemaVersionHeader)
		secureJSONHeaders(response)
		switch request.URL.Path {
		case "/handshake":
			writeTestJSON(t, response, Handshake{
				SchemaVersion: contractv2.SchemaVersion, DaemonInstanceID: snapshot.Daemon.InstanceID,
				DaemonVersion: snapshot.Daemon.Version, SupportedSchemaVersions: []int{contractv2.SchemaVersion},
			})
		case "/v1/status":
			writeTestJSON(t, response, snapshot)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/handshake", "/v1/status"} {
		if declared[path] != "2" {
			t.Fatalf("%s declared %q, want exact schema version 2", path, declared[path])
		}
	}
}

func TestClientMapsHTTP426ToUpgradeRequired(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	tests := []struct {
		name        string
		body        string
		wantMessage string
	}{
		{
			name:        "readable envelope keeps bounded context",
			body:        `{"schemaVersion":2,"error":{"code":"UPGRADE_REQUIRED","message":"This client's contract schema version is not supported by the daemon.","retryable":false,"currentState":"3","requestedState":"2","nextAction":"upgrade_client"}}`,
			wantMessage: "This client's contract schema version is not supported by the daemon.",
		},
		{name: "unreadable envelope still reports the stable code", body: `not json`},
		{name: "foreign code is not trusted as context", body: `{"schemaVersion":2,"error":{"code":"SOMETHING_ELSE","message":"x","retryable":false}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				secureJSONHeaders(response)
				response.WriteHeader(http.StatusUpgradeRequired)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
			_, err := client.Status(context.Background())
			if CodeOf(err) != ErrorUpgradeRequired {
				t.Fatalf("code: got %q, want UPGRADE_REQUIRED (%v)", CodeOf(err), err)
			}
			contractError, ok := ContractErrorOf(err)
			if test.wantMessage == "" {
				if ok {
					t.Fatalf("unexpected contract context %+v", contractError)
				}
				return
			}
			if !ok || contractError.Message != test.wantMessage || contractError.NextAction != "upgrade_client" ||
				contractError.CurrentState != "3" {
				t.Fatalf("contract context: %+v (ok=%v)", contractError, ok)
			}
		})
	}
}

func TestHandshakeSchemaMismatchIsUpgradeRequiredNotGeneric(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	tests := []struct {
		name      string
		handshake Handshake
		wantCode  ErrorCode
	}{
		{
			name: "daemon publishes a newer schema",
			handshake: Handshake{
				SchemaVersion: 3, DaemonInstanceID: snapshot.Daemon.InstanceID,
				DaemonVersion: snapshot.Daemon.Version, SupportedSchemaVersions: []int{3},
			},
			wantCode: ErrorUpgradeRequired,
		},
		{
			name: "daemon publishes only an older schema",
			handshake: Handshake{
				SchemaVersion: 2, DaemonInstanceID: snapshot.Daemon.InstanceID,
				DaemonVersion: snapshot.Daemon.Version, SupportedSchemaVersions: []int{1},
			},
			wantCode: ErrorUpgradeRequired,
		},
		{
			name: "zero schema is malformed rather than an upgrade",
			handshake: Handshake{
				SchemaVersion: 0, DaemonInstanceID: snapshot.Daemon.InstanceID,
				DaemonVersion: snapshot.Daemon.Version, SupportedSchemaVersions: []int{2},
			},
			wantCode: ErrorDaemonResponseInvalid,
		},
		{
			name: "different build of the same schema is incompatible",
			handshake: Handshake{
				SchemaVersion: 2, DaemonInstanceID: snapshot.Daemon.InstanceID,
				DaemonVersion: "9.9.9", SupportedSchemaVersions: []int{2},
			},
			wantCode: ErrorDaemonIncompatible,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				secureJSONHeaders(response)
				writeTestJSON(t, response, test.handshake)
			}))
			defer server.Close()
			client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
			_, err := client.Handshake(context.Background())
			if CodeOf(err) != test.wantCode {
				t.Fatalf("code: got %q, want %q", CodeOf(err), test.wantCode)
			}
		})
	}
}

func TestDescriptorFromAnotherContractGenerationIsUpgradeRequired(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		version  int
		wantCode ErrorCode
	}{
		{name: "older descriptor", version: 1, wantCode: ErrorUpgradeRequired},
		{name: "newer descriptor", version: 3, wantCode: ErrorUpgradeRequired},
		{name: "missing schema is invalid", version: 0, wantCode: ErrorRuntimeDescriptorInvalid},
		{name: "negative schema is invalid", version: -2, wantCode: ErrorRuntimeDescriptorInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := writeRuntimeFiles(t, RuntimeDescriptor{
				SchemaVersion: test.version, Endpoint: "http://127.0.0.1:43123",
				DaemonInstanceID: "daemon_test", DaemonVersion: "0.1.0-dev", PID: 123,
				ProcessStartedAt: now.Add(-time.Hour), GeneratedAt: now.Add(-time.Hour),
			}, testToken())
			_, err := Discover(paths, DiscoveryPolicy{Now: func() time.Time { return now }})
			if CodeOf(err) != test.wantCode {
				t.Fatalf("code: got %q, want %q (%v)", CodeOf(err), test.wantCode, err)
			}
			if err != nil && strings.Contains(err.Error(), testToken()) {
				t.Fatal("error disclosed the token")
			}
		})
	}
}

func TestDoctorNamesUpgradeRequirement(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	paths := writeRuntimeFiles(t, RuntimeDescriptor{
		SchemaVersion: 1, Endpoint: "http://127.0.0.1:43123",
		DaemonInstanceID: "daemon_test", DaemonVersion: "0.1.0-dev", PID: 123,
		ProcessStartedAt: now.Add(-time.Hour), GeneratedAt: now.Add(-time.Hour),
	}, testToken())
	report := Doctor{Connector: Connector{Paths: paths, DiscoveryPolicy: DiscoveryPolicy{Now: func() time.Time { return now }}}}.Run(context.Background())
	if report.Healthy || len(report.Checks) == 0 {
		t.Fatalf("report: %+v", report)
	}
	first := report.Checks[0]
	if first.ID != "runtime.discovery" || first.ErrorCode != ErrorUpgradeRequired || first.Summary != upgradeRequiredSummary {
		t.Fatalf("discovery check: %+v", first)
	}
}
