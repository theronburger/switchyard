package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/apiclient"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

type stubServerBackend struct {
	snapshot contractv1.StatusSnapshot
	status   error
	doctor   apiclient.DoctorReport
}

func (b stubServerBackend) Status(context.Context) (contractv1.StatusSnapshot, error) {
	return b.snapshot, b.status
}

func (b stubServerBackend) Doctor(context.Context) apiclient.DoctorReport {
	return b.doctor
}

func TestServerInitializesListsToolsAndReturnsScopedFooter(t *testing.T) {
	snapshot := serverSnapshot()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"switchyard_status","arguments":{"environmentId":"env_test"}}}`,
	}, "\n") + "\n"
	responses := runServer(t, stubServerBackend{snapshot: snapshot}, input)
	if len(responses) != 3 {
		t.Fatalf("responses: got %d, want 3", len(responses))
	}

	var initialized struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	decodeResponse(t, responses[0], &initialized)
	if initialized.Result.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol: got %q", initialized.Result.ProtocolVersion)
	}

	var listed struct {
		Result struct {
			Tools []toolDefinition `json:"tools"`
		} `json:"result"`
	}
	decodeResponse(t, responses[1], &listed)
	if len(listed.Result.Tools) != 2 || listed.Result.Tools[0].Name != "switchyard_status" {
		t.Fatalf("tools: %+v", listed.Result.Tools)
	}

	var called struct {
		Result struct {
			StructuredContent statusOutput `json:"structuredContent"`
			IsError           bool         `json:"isError"`
		} `json:"result"`
	}
	decodeResponse(t, responses[2], &called)
	if called.Result.IsError {
		t.Fatal("status tool returned an error")
	}
	footer := called.Result.StructuredContent.EnvironmentContext
	if footer == nil || footer.EnvironmentID != "env_test" || footer.AttentionCount != 1 {
		t.Fatalf("footer: %+v", footer)
	}
}

func TestServerGlobalStatusHasNoImplicitFooter(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"switchyard_status","arguments":{}}}`,
	}, "\n") + "\n"
	responses := runServer(t, stubServerBackend{snapshot: serverSnapshot()}, input)
	if bytes.Contains(responses[1], []byte("environmentContext")) {
		t.Fatal("global status returned an implicit environment footer")
	}
}

func TestServerReturnsDoctorAsToolErrorWhenUnhealthy(t *testing.T) {
	report := apiclient.DoctorReport{
		SchemaVersion: 1,
		GeneratedAt:   time.Now(),
		Healthy:       false,
		Checks: []apiclient.DoctorCheck{{
			ID:        "daemon.handshake",
			Status:    apiclient.CheckFail,
			Summary:   "The installed daemon could not be authenticated.",
			ErrorCode: apiclient.ErrorDaemonUnknown,
		}},
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"switchyard_doctor","arguments":{}}}`,
	}, "\n") + "\n"
	responses := runServer(t, stubServerBackend{doctor: report}, input)
	var called struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	decodeResponse(t, responses[1], &called)
	if !called.Result.IsError {
		t.Fatal("unhealthy doctor result was not marked as a tool error")
	}
}

func TestServerRedactsBackendErrors(t *testing.T) {
	secret := "secret-bearer-token"
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"switchyard_status","arguments":{}}}`,
	}, "\n") + "\n"
	responses := runServer(t, stubServerBackend{status: errors.New("failed with " + secret)}, input)
	if bytes.Contains(bytes.Join(responses, nil), []byte(secret)) {
		t.Fatal("MCP response leaked backend error contents")
	}
}

func TestServerRequiresInitializationAndRejectsUnknownTools(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"foreign_tool","arguments":{}}}`,
	}, "\n") + "\n"
	responses := runServer(t, stubServerBackend{}, input)
	var first response
	decodeResponse(t, responses[0], &first)
	if first.Error == nil || first.Error.Code != -32600 {
		t.Fatalf("pre-initialize response: %+v", first)
	}
	var last response
	decodeResponse(t, responses[2], &last)
	if last.Error == nil || last.Error.Code != -32602 {
		t.Fatalf("unknown tool response: %+v (%s)", last, responses[2])
	}
}

func runServer(t *testing.T, backend Backend, input string) [][]byte {
	t.Helper()
	var output bytes.Buffer
	server := Server{Backend: backend, Name: "switchyard", Version: "test"}
	if err := server.Run(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) == 1 && len(lines[0]) == 0 {
		return nil
	}
	return lines
}

func decodeResponse(t *testing.T, contents []byte, destination any) {
	t.Helper()
	if err := json.Unmarshal(contents, destination); err != nil {
		t.Fatalf("decode response: %v (%s)", err, contents)
	}
}

func serverSnapshot() contractv1.StatusSnapshot {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
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
		Environments: []contractv1.Environment{{
			ID:                "env_test",
			Revision:          17,
			DesiredState:      "running",
			ObservedState:     "running",
			Health:            "degraded",
			URLs:              map[string]string{"organizer": "http://127.0.0.1:7005"},
			AttentionAlertIDs: []string{"alert_test"},
		}},
		Alerts: []contractv1.Alert{{
			ID:            "alert_test",
			EnvironmentID: "env_test",
			Severity:      "error",
			Code:          "SERVICE_EXITED",
			Summary:       "Service exited.",
			Status:        "active",
			FirstSeenAt:   now,
			LastSeenAt:    now,
			Occurrences:   1,
		}},
	}
}
