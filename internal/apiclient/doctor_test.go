package apiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

func TestDoctorReportsStructuredPassingChecks(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	server := healthyDaemonServer(t, token, snapshot)
	defer server.Close()
	paths := writeRuntimeFiles(t, RuntimeDescriptor{
		SchemaVersion:    RuntimeDescriptorSchemaVersion,
		Endpoint:         server.URL,
		DaemonInstanceID: snapshot.Daemon.InstanceID,
		DaemonVersion:    snapshot.Daemon.Version,
		PID:              123,
		ProcessStartedAt: now.Add(-time.Hour),
		GeneratedAt:      now,
	}, token)

	report := (Doctor{
		Connector: Connector{Paths: paths, DiscoveryPolicy: DiscoveryPolicy{Now: func() time.Time { return now }}},
		Now:       func() time.Time { return now },
	}).Run(context.Background())
	if !report.Healthy {
		t.Fatalf("report was unhealthy: %+v", report.Checks)
	}
	if len(report.Checks) != 3 {
		t.Fatalf("checks: got %d, want 3", len(report.Checks))
	}
	for _, check := range report.Checks {
		if check.Status != CheckPass {
			t.Fatalf("check %q: got %q", check.ID, check.Status)
		}
	}
}

func TestDoctorReportsUnknownDaemonWithoutLeakingSecrets(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		writeTestJSON(t, response, Handshake{
			SchemaVersion:           RuntimeDescriptorSchemaVersion,
			DaemonInstanceID:        "daemon_foreign",
			DaemonVersion:           snapshot.Daemon.Version,
			SupportedSchemaVersions: []int{contractv2.SchemaVersion},
		})
	}))
	defer server.Close()
	paths := writeRuntimeFiles(t, RuntimeDescriptor{
		SchemaVersion:    RuntimeDescriptorSchemaVersion,
		Endpoint:         server.URL,
		DaemonInstanceID: snapshot.Daemon.InstanceID,
		DaemonVersion:    snapshot.Daemon.Version,
		PID:              123,
		ProcessStartedAt: now.Add(-time.Hour),
		GeneratedAt:      now,
	}, token)

	report := (Doctor{
		Connector: Connector{Paths: paths, DiscoveryPolicy: DiscoveryPolicy{Now: func() time.Time { return now }}},
		Now:       func() time.Time { return now },
	}).Run(context.Background())
	if report.Healthy {
		t.Fatal("unknown daemon reported healthy")
	}
	if report.Checks[1].ErrorCode != ErrorDaemonUnknown {
		t.Fatalf("error code: got %q", report.Checks[1].ErrorCode)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatal("doctor JSON leaked bearer token")
	}
}

func TestDoctorSkipsDaemonChecksWhenRuntimeFilesAreMissing(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	report := (Doctor{
		Connector: Connector{Paths: RuntimePaths{
			Descriptor: "/missing/switchyard-runtime.json",
			Token:      "/missing/switchyard-token",
		}},
		Now: func() time.Time { return now },
	}).Run(context.Background())
	if report.Checks[0].ErrorCode != ErrorRuntimeDescriptorUnavailable {
		t.Fatalf("discovery error code: got %q", report.Checks[0].ErrorCode)
	}
	if report.Checks[1].Status != CheckSkipped || report.Checks[2].Status != CheckSkipped {
		t.Fatalf("downstream checks were not skipped: %+v", report.Checks)
	}
}

func healthyDaemonServer(t *testing.T, token string, snapshot contractv2.StatusSnapshot) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		secureJSONHeaders(response)
		switch request.URL.Path {
		case "/handshake":
			writeTestJSON(t, response, Handshake{
				SchemaVersion:           RuntimeDescriptorSchemaVersion,
				DaemonInstanceID:        snapshot.Daemon.InstanceID,
				DaemonVersion:           snapshot.Daemon.Version,
				SupportedSchemaVersions: []int{contractv2.SchemaVersion},
			})
		case "/v1/status":
			writeTestJSON(t, response, snapshot)
		default:
			http.NotFound(response, request)
		}
	}))
}
