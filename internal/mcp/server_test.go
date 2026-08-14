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
	start    func(context.Context, contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error)
	stop     func(context.Context, string, contractv1.StopEnvironmentRequest) (contractv1.MutationReceipt, error)
}

const modernMetadata = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"test-client","version":"1.0.0"}}`

func (b stubServerBackend) Status(context.Context) (contractv1.StatusSnapshot, error) {
	return b.snapshot, b.status
}

func (b stubServerBackend) Doctor(context.Context) apiclient.DoctorReport {
	return b.doctor
}

func (b stubServerBackend) StartEnvironment(
	ctx context.Context,
	request contractv1.StartEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	if b.start == nil {
		return contractv1.MutationReceipt{}, errors.New("start is not configured")
	}
	return b.start(ctx, request)
}

func (b stubServerBackend) StopEnvironment(
	ctx context.Context,
	environmentID string,
	request contractv1.StopEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	if b.stop == nil {
		return contractv1.MutationReceipt{}, errors.New("stop is not configured")
	}
	return b.stop(ctx, environmentID, request)
}

func TestServerInitializesListsToolsAndReturnsScopedFooter(t *testing.T) {
	snapshot := serverSnapshot()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
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
	if initialized.Result.ProtocolVersion != LegacyProtocolVersion {
		t.Fatalf("protocol: got %q", initialized.Result.ProtocolVersion)
	}

	var listed struct {
		Result struct {
			Tools []toolDefinition `json:"tools"`
		} `json:"result"`
	}
	decodeResponse(t, responses[1], &listed)
	if len(listed.Result.Tools) != 4 || listed.Result.Tools[0].Name != "switchyard_status" {
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

func TestServerSupportsStatelessModernDiscoveryListAndCall(t *testing.T) {
	snapshot := serverSnapshot()
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{` + modernMetadata + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{` + modernMetadata + `}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"switchyard_status","arguments":{"environmentId":"env_test"},` + modernMetadata + `}}`,
	}, "\n") + "\n"
	responses := runServer(t, stubServerBackend{snapshot: snapshot}, input)
	if len(responses) != 3 {
		t.Fatalf("responses: got %d, want 3", len(responses))
	}

	var discovered struct {
		Result struct {
			ResultType        string                            `json:"resultType"`
			SupportedVersions []string                          `json:"supportedVersions"`
			Capabilities      map[string]json.RawMessage        `json:"capabilities"`
			TTLMilliseconds   int                               `json:"ttlMs"`
			CacheScope        string                            `json:"cacheScope"`
			Meta              map[string]implementationMetadata `json:"_meta"`
		} `json:"result"`
	}
	decodeResponse(t, responses[0], &discovered)
	if discovered.Result.ResultType != "complete" ||
		len(discovered.Result.SupportedVersions) != 1 ||
		discovered.Result.SupportedVersions[0] != ProtocolVersion ||
		discovered.Result.CacheScope != "public" {
		t.Fatalf("discover result: %+v", discovered.Result)
	}
	if _, exists := discovered.Result.Capabilities["tools"]; !exists {
		t.Fatal("discover did not advertise tools")
	}
	if serverInfo := discovered.Result.Meta[metaServerInfo]; serverInfo.Name != "switchyard" || serverInfo.Version != "test" {
		t.Fatalf("server info: %+v", serverInfo)
	}

	var listed struct {
		Result struct {
			ResultType string           `json:"resultType"`
			Tools      []toolDefinition `json:"tools"`
			CacheScope string           `json:"cacheScope"`
			Meta       map[string]any   `json:"_meta"`
		} `json:"result"`
	}
	decodeResponse(t, responses[1], &listed)
	if listed.Result.ResultType != "complete" || len(listed.Result.Tools) != 4 ||
		listed.Result.CacheScope != "public" || listed.Result.Meta[metaServerInfo] == nil {
		t.Fatalf("modern tools list: %+v", listed.Result)
	}

	var called struct {
		Result struct {
			ResultType        string         `json:"resultType"`
			StructuredContent statusOutput   `json:"structuredContent"`
			Meta              map[string]any `json:"_meta"`
		} `json:"result"`
	}
	decodeResponse(t, responses[2], &called)
	footer := called.Result.StructuredContent.EnvironmentContext
	if called.Result.ResultType != "complete" || called.Result.Meta[metaServerInfo] == nil ||
		footer == nil || footer.EnvironmentID != "env_test" {
		t.Fatalf("modern tool call: %+v", called.Result)
	}
}

func TestServerRequiresModernMetadataOnEveryRequest(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{` + modernMetadata + `}}`,
		`{"jsonrpc":"2.0","id":2,"method":"server/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"},` + modernMetadata + `}}`,
	}, "\n") + "\n"
	responses := runServer(t, stubServerBackend{}, input)
	if len(responses) != 6 {
		t.Fatalf("responses: got %d, want 6", len(responses))
	}

	wantCodes := []int{0, -32602, -32600, -32602, -32022, -32601}
	for index, want := range wantCodes {
		var decoded response
		decodeResponse(t, responses[index], &decoded)
		if want == 0 {
			if decoded.Error != nil {
				t.Fatalf("response %d: unexpected error %+v", index, decoded.Error)
			}
			continue
		}
		if decoded.Error == nil || decoded.Error.Code != want {
			t.Fatalf("response %d: got %+v, want code %d", index, decoded.Error, want)
		}
	}
	var unsupported response
	decodeResponse(t, responses[4], &unsupported)
	data, ok := unsupported.Error.Data.(map[string]any)
	if !ok || data["requested"] != "2099-01-01" {
		t.Fatalf("unsupported version data: %#v", unsupported.Error.Data)
	}
}

func TestServerNegotiatesBothLegacyProtocolVersions(t *testing.T) {
	for _, version := range []string{LegacyProtocolVersion, LegacyPreviousProtocolVersion} {
		t.Run(version, func(t *testing.T) {
			input := strings.Join([]string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + version + `","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
			}, "\n") + "\n"
			responses := runServer(t, stubServerBackend{}, input)
			var initialized struct {
				Result initializeResult `json:"result"`
			}
			decodeResponse(t, responses[0], &initialized)
			if initialized.Result.ProtocolVersion != version {
				t.Fatalf("protocol: got %q, want %q", initialized.Result.ProtocolVersion, version)
			}
			if bytes.Contains(responses[1], []byte(`"resultType"`)) ||
				bytes.Contains(responses[1], []byte(metaServerInfo)) {
				t.Fatal("legacy response included modern result fields")
			}
		})
	}
}

func TestServerGlobalStatusHasNoImplicitFooter(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
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
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
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
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"switchyard_status","arguments":{}}}`,
	}, "\n") + "\n"
	responses := runServer(t, stubServerBackend{status: errors.New("failed with " + secret)}, input)
	if bytes.Contains(bytes.Join(responses, nil), []byte(secret)) {
		t.Fatal("MCP response leaked backend error contents")
	}
}

func TestServerSubmitsThinStartAndReturnsStateFooter(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 14, 16, 0, 0, 0, time.UTC)
	backend := stubServerBackend{
		snapshot: serverSnapshot(),
		start: func(_ context.Context, request contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error) {
			if request.Validate() != nil || request.RequestID != "request_start" ||
				request.IdempotencyKey != "agent:retry" || request.WorktreeID != "worktree_test" ||
				len(request.ServiceIDs) != 2 {
				t.Fatalf("start request: %+v", request)
			}
			return contractv1.MutationReceipt{
				SchemaVersion: contractv1.SchemaVersion, RequestID: request.RequestID,
				OperationID: "operation_start", AcceptedAt: acceptedAt, EnvironmentID: "env_test",
			}, nil
		},
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"switchyard_start","arguments":{"requestId":"request_start","idempotencyKey":"agent:retry","worktreeId":"worktree_test","serviceIds":["organizer","nonprofit-service"]}}}`,
	}, "\n") + "\n"
	responses := runServer(t, backend, input)
	var called struct {
		Result struct {
			StructuredContent mutationOutput `json:"structuredContent"`
			IsError           bool           `json:"isError"`
		} `json:"result"`
	}
	decodeResponse(t, responses[1], &called)
	if called.Result.IsError || called.Result.StructuredContent.Receipt.OperationID != "operation_start" {
		t.Fatalf("start result: %+v", called.Result)
	}
	footer := called.Result.StructuredContent.EnvironmentContext
	if footer == nil || footer.EnvironmentID != "env_test" || footer.AttentionCount != 1 {
		t.Fatalf("start footer: %+v", footer)
	}
}

func TestServerSubmitsStopWithoutWaiting(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 14, 16, 0, 0, 0, time.UTC)
	backend := stubServerBackend{
		snapshot: serverSnapshot(),
		stop: func(_ context.Context, environmentID string, request contractv1.StopEnvironmentRequest) (contractv1.MutationReceipt, error) {
			if environmentID != "env_test" || request.Validate() != nil ||
				request.ExpectedEnvironmentRevision == nil || *request.ExpectedEnvironmentRevision != 17 {
				t.Fatalf("stop environment=%q request=%+v", environmentID, request)
			}
			return contractv1.MutationReceipt{
				SchemaVersion: contractv1.SchemaVersion, RequestID: request.RequestID,
				OperationID: "operation_stop", AcceptedAt: acceptedAt, EnvironmentID: environmentID,
			}, nil
		},
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"switchyard_stop","arguments":{"requestId":"request_stop","idempotencyKey":"agent:stop","environmentId":"env_test","expectedEnvironmentRevision":17}}}`,
	}, "\n") + "\n"
	responses := runServer(t, backend, input)
	if !bytes.Contains(responses[1], []byte(`"operationId":"operation_stop"`)) {
		t.Fatalf("stop result: %s", responses[1])
	}
}

func TestServerRejectsInvalidActionArgumentsBeforeBackend(t *testing.T) {
	calls := 0
	backend := stubServerBackend{
		start: func(context.Context, contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error) {
			calls++
			return contractv1.MutationReceipt{}, nil
		},
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"switchyard_start","arguments":{"requestId":"request","idempotencyKey":"key","worktreeId":"worktree","serviceIds":null}}}`,
	}, "\n") + "\n"
	responses := runServer(t, backend, input)
	var decoded response
	decodeResponse(t, responses[1], &decoded)
	if decoded.Error == nil || decoded.Error.Code != -32602 || calls != 0 {
		t.Fatalf("invalid action response=%+v calls=%d", decoded, calls)
	}
}

func TestServerRedactsActionBackendErrors(t *testing.T) {
	secret := "/Users/person/state.sqlite bearer-secret"
	backend := stubServerBackend{
		start: func(context.Context, contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error) {
			return contractv1.MutationReceipt{}, errors.New(secret)
		},
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"switchyard_start","arguments":{"requestId":"request","idempotencyKey":"key","worktreeId":"worktree","serviceIds":["organizer"]}}}`,
	}, "\n") + "\n"
	responses := runServer(t, backend, input)
	if bytes.Contains(bytes.Join(responses, nil), []byte(secret)) ||
		!bytes.Contains(responses[1], []byte(`"isError":true`)) {
		t.Fatalf("action error response: %s", responses[1])
	}
}

func TestServerRequiresInitializationAndRejectsUnknownTools(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0.0"}}}`,
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
