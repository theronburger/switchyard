package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

func decodeErrorEnvelope(t *testing.T, response *httptest.ResponseRecorder) errorResponse {
	t.Helper()
	var envelope errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, response.Body.String())
	}
	return envelope
}

func TestVersionedRoutesRequireAnExactSchemaDeclaration(t *testing.T) {
	handler := newTestHandler(t, validHTTPStatus())
	tests := []struct {
		name       string
		declared   []string
		wantStatus int
		wantNext   string
		wantReq    string
	}{
		{name: "exact", declared: []string{"2"}, wantStatus: http.StatusOK},
		{name: "undeclared", declared: nil, wantStatus: http.StatusUpgradeRequired, wantNext: "upgrade_client"},
		{name: "older client", declared: []string{"1"}, wantStatus: http.StatusUpgradeRequired, wantNext: "upgrade_client", wantReq: "1"},
		{name: "newer client", declared: []string{"3"}, wantStatus: http.StatusUpgradeRequired, wantNext: "upgrade_daemon", wantReq: "3"},
		{name: "padded", declared: []string{"02"}, wantStatus: http.StatusUpgradeRequired, wantNext: "upgrade_client"},
		{name: "unreadable", declared: []string{"two"}, wantStatus: http.StatusUpgradeRequired, wantNext: "upgrade_client"},
		{name: "duplicate", declared: []string{"2", "2"}, wantStatus: http.StatusUpgradeRequired, wantNext: "upgrade_client"},
		{name: "huge", declared: []string{"9999999999"}, wantStatus: http.StatusUpgradeRequired, wantNext: "upgrade_client"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
			request.Header.Set("Authorization", "Bearer "+testToken)
			for _, value := range test.declared {
				request.Header.Add(contractv2.SchemaVersionHeader, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status: got %d, want %d (%s)", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantStatus != http.StatusUpgradeRequired {
				return
			}
			envelope := decodeErrorEnvelope(t, response)
			if envelope.SchemaVersion != contractv2.SchemaVersion || envelope.Error.Code != contractv2.UpgradeRequiredCode {
				t.Fatalf("envelope: %+v", envelope)
			}
			if envelope.Error.Retryable {
				t.Fatal("an upgrade requirement is not retryable")
			}
			if envelope.Error.CurrentState != "2" || envelope.Error.RequestedState != test.wantReq || envelope.Error.NextAction != test.wantNext {
				t.Fatalf("context: %+v", envelope.Error)
			}
			if strings.Contains(response.Body.String(), testToken) {
				t.Fatal("upgrade response disclosed the token")
			}
		})
	}
}

func TestUnauthenticatedRequestsAreRejectedBeforeVersionNegotiation(t *testing.T) {
	handler := newTestHandler(t, validHTTPStatus())
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	request.Header.Set(contractv2.SchemaVersionHeader, "1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", response.Code)
	}
}

func TestHandshakeToleratesUndeclaredClientsButRejectsMismatches(t *testing.T) {
	handler := newTestHandler(t, validHTTPStatus())
	undeclared := httptest.NewRequest(http.MethodGet, "/handshake", nil)
	undeclared.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, undeclared)
	if response.Code != http.StatusOK {
		t.Fatalf("undeclared handshake status: %d", response.Code)
	}

	mismatched := httptest.NewRequest(http.MethodGet, "/handshake", nil)
	mismatched.Header.Set("Authorization", "Bearer "+testToken)
	mismatched.Header.Set(contractv2.SchemaVersionHeader, "1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, mismatched)
	if response.Code != http.StatusUpgradeRequired {
		t.Fatalf("mismatched handshake status: %d", response.Code)
	}
	envelope := decodeErrorEnvelope(t, response)
	if envelope.Error.Code != contractv2.UpgradeRequiredCode || envelope.Error.RequestedState != "1" {
		t.Fatalf("envelope: %+v", envelope.Error)
	}
}

func TestMutationBodiesFromAnotherContractGenerationAreUpgradeRequired(t *testing.T) {
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: testToken, DaemonInstanceID: "daemon_01", DaemonVersion: "0.1.0-dev", StartedAt: time.Now(),
		StatusSource:  staticStatusSource{snapshot: validHTTPStatus()},
		Cleanup:       cleanupHTTPBackend{},
		Configuration: &configurationActionBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name: "v1 start body with unknown field", path: "/v1/environments",
			body:       `{"schemaVersion":1,"requestId":"request_1","idempotencyKey":"start:1","worktreeId":"worktree_1","adapter":"legacy","serviceIds":["storefront"]}`,
			wantStatus: http.StatusUpgradeRequired, wantCode: contractv2.UpgradeRequiredCode,
		},
		{
			name: "v3 start body", path: "/v1/environments",
			body:       `{"schemaVersion":3,"requestId":"request_1","idempotencyKey":"start:1","worktreeId":"worktree_1","serviceIds":["storefront"]}`,
			wantStatus: http.StatusUpgradeRequired, wantCode: contractv2.UpgradeRequiredCode,
		},
		{
			name: "missing schema version stays invalid", path: "/v1/environments",
			body:       `{"requestId":"request_1","idempotencyKey":"start:1","worktreeId":"worktree_1","serviceIds":["storefront"]}`,
			wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST",
		},
		{
			name: "zero schema version stays invalid", path: "/v1/environments",
			body:       `{"schemaVersion":0,"requestId":"request_1","idempotencyKey":"start:1","worktreeId":"worktree_1","serviceIds":["storefront"]}`,
			wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST",
		},
		{
			name: "string schema version stays invalid", path: "/v1/environments",
			body:       `{"schemaVersion":"2","requestId":"request_1","idempotencyKey":"start:1","worktreeId":"worktree_1","serviceIds":["storefront"]}`,
			wantStatus: http.StatusBadRequest, wantCode: "INVALID_REQUEST",
		},
		{
			name: "v1 cleanup plan", path: "/v1/cleanup/plans",
			body:       `{"schemaVersion":1,"scope":{"kind":"global"}}`,
			wantStatus: http.StatusUpgradeRequired, wantCode: contractv2.UpgradeRequiredCode,
		},
		{
			name: "v1 configuration acceptance", path: "/v1/configuration/accept",
			body:       `{"schemaVersion":1,"expectedRevision":0,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}`,
			wantStatus: http.StatusUpgradeRequired, wantCode: contractv2.UpgradeRequiredCode,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer "+testToken)
			request.Header.Set(contractv2.SchemaVersionHeader, "2")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status: got %d, want %d (%s)", response.Code, test.wantStatus, response.Body.String())
			}
			envelope := decodeErrorEnvelope(t, response)
			if envelope.Error.Code != test.wantCode {
				t.Fatalf("code: got %q, want %q", envelope.Error.Code, test.wantCode)
			}
		})
	}
}

func TestExactSchemaBodiesStillValidateStrictly(t *testing.T) {
	handler := newTestHandler(t, validHTTPStatus())
	request := httptest.NewRequest(http.MethodPost, "/v1/environments", strings.NewReader(
		`{"schemaVersion":2,"requestId":"request_1","idempotencyKey":"start:1","worktreeId":"worktree_1","serviceIds":[]}`,
	))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set(contractv2.SchemaVersionHeader, "2")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400 (%s)", response.Code, response.Body.String())
	}
	if envelope := decodeErrorEnvelope(t, response); envelope.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("code: %q", envelope.Error.Code)
	}
}
