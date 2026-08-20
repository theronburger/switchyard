package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

func TestClientReadsExplicitOperationDiagnosticsAfterHandshake(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		if request.URL.Path == "/handshake" {
			writeTestHandshake(t, response, snapshot)
			return
		}
		if request.URL.Path != "/v1/operations/operation_01/diagnostics" || request.URL.Query().Get("maxBytes") != "2048" {
			t.Errorf("diagnostics request: %s?%s", request.URL.Path, request.URL.RawQuery)
		}
		writeTestJSON(t, response, contractv2.OperationDiagnostics{
			SchemaVersion: contractv2.SchemaVersion, OperationID: "operation_01",
			EnvironmentID: "env_01", LogReference: "run_01/preparations/service/command-0",
			Excerpts: []contractv2.OperationLogExcerpt{{Stream: "stderr", Content: "TS2304", Truncated: false, Redacted: true}},
		})
	}))
	defer server.Close()
	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	diagnostics, err := client.OperationDiagnostics(context.Background(), "operation_01", 2048)
	if err != nil || diagnostics.OperationID != "operation_01" || len(diagnostics.Excerpts) != 1 {
		t.Fatalf("diagnostics: %+v err=%v", diagnostics, err)
	}
}

func TestClientAcceptsProfileActionDiagnosticsWithoutEnvironment(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		if request.URL.Path == "/handshake" {
			writeTestHandshake(t, response, snapshot)
			return
		}
		writeTestJSON(t, response, contractv2.OperationDiagnostics{
			SchemaVersion: contractv2.SchemaVersion, OperationID: "operation_11",
			LogReference: "sample/operation_11",
			Excerpts:     []contractv2.OperationLogExcerpt{{Stream: "stdout", Content: "tidy: 3 files changed"}},
		})
	}))
	defer server.Close()
	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	diagnostics, err := client.OperationDiagnostics(context.Background(), "operation_11", 0)
	if err != nil || diagnostics.EnvironmentID != "" || len(diagnostics.Excerpts) != 1 {
		t.Fatalf("diagnostics: %+v err=%v", diagnostics, err)
	}
}

func TestClientPreservesDiagnosticsContractError(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		if request.URL.Path == "/handshake" {
			writeTestHandshake(t, response, snapshot)
			return
		}
		response.WriteHeader(http.StatusConflict)
		writeTestJSON(t, response, mutationErrorResponse{
			SchemaVersion: contractv2.SchemaVersion,
			Error:         contractv2.ContractError{Code: "DIAGNOSTICS_UNAVAILABLE", Message: "This operation has no available diagnostics", Retryable: false},
		})
	}))
	defer server.Close()
	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	_, err := client.OperationDiagnostics(context.Background(), "operation_01", 0)
	contractError, ok := ContractErrorOf(err)
	if !ok || contractError.Code != "DIAGNOSTICS_UNAVAILABLE" || contractError.Retryable {
		t.Fatalf("contract error: %+v ok=%t err=%v", contractError, ok, err)
	}
}
