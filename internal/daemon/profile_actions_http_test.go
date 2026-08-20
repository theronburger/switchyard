package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

type profileActionsHTTPBackend struct {
	runs []contractv1.RunProfileActionRequest
}

func (backend *profileActionsHTTPBackend) ListActions(context.Context) (contractv1.ProfileActionList, error) {
	return contractv1.ProfileActionList{
		SchemaVersion: contractv1.SchemaVersion, AcceptedDigest: "sha256:" + strings.Repeat("a", 64),
		Actions: []contractv1.ProfileAction{{
			ID: "tidy", RepositoryID: "repository_01", ProfileKey: "sample", ProfileDigest: "sha256:" + strings.Repeat("a", 64),
			DisplayName: "Tidy", Scope: "worktree", Risk: "local", Kind: "command",
		}},
	}, nil
}

func (backend *profileActionsHTTPBackend) RunAction(_ context.Context, request contractv1.RunProfileActionRequest) (contractv1.MutationReceipt, error) {
	backend.runs = append(backend.runs, request)
	if request.ActionID == "push" {
		return contractv1.MutationReceipt{}, &ActionError{Status: http.StatusConflict, Contract: contractv1.ContractError{
			Code: "ACTION_CONFIRMATION_REQUIRED", Message: "confirm", ResourceKind: "action", ResourceID: "push",
		}}
	}
	return contractv1.MutationReceipt{
		SchemaVersion: contractv1.SchemaVersion, RequestID: request.RequestID, OperationID: "operation_01",
		AcceptedAt: time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC),
	}, nil
}

func profileActionsHandler(t *testing.T, backend ProfileActions) http.Handler {
	t.Helper()
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: "secret", DaemonInstanceID: "daemon_01", DaemonVersion: "test", StartedAt: time.Now(),
		StatusSource: staticStatusSource{snapshot: validHTTPStatus()}, ProfileActions: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serveProfileActions(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestProfileActionsHTTPListsAndRunsActions(t *testing.T) {
	backend := &profileActionsHTTPBackend{}
	handler := profileActionsHandler(t, backend)
	response := serveProfileActions(handler, http.MethodGet, "/v1/actions", "")
	if response.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", response.Code, response.Body.String())
	}
	var list contractv1.ProfileActionList
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil || list.Validate() != nil || len(list.Actions) != 1 {
		t.Fatalf("list body: %v %s", err, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "executable") || strings.Contains(response.Body.String(), "arguments") {
		t.Fatal("action list exposed command shape")
	}
	response = serveProfileActions(handler, http.MethodPost, "/v1/actions/run",
		`{"schemaVersion":1,"requestId":"request_01","idempotencyKey":"key_01","repositoryId":"repository_01","actionId":"tidy","worktreeId":"worktree_01"}`)
	if response.Code != http.StatusAccepted || len(backend.runs) != 1 || backend.runs[0].WorktreeID != "worktree_01" {
		t.Fatalf("run: status=%d body=%s runs=%+v", response.Code, response.Body.String(), backend.runs)
	}
	response = serveProfileActions(handler, http.MethodPost, "/v1/actions/run",
		`{"schemaVersion":1,"requestId":"request_02","idempotencyKey":"key_02","repositoryId":"repository_01","actionId":"push"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "ACTION_CONFIRMATION_REQUIRED") {
		t.Fatalf("confirmation: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestProfileActionsHTTPRejectsInvalidRequests(t *testing.T) {
	backend := &profileActionsHTTPBackend{}
	handler := profileActionsHandler(t, backend)
	cases := map[string]struct {
		method, path, body string
		status             int
	}{
		"list with query":                 {http.MethodGet, "/v1/actions?repositoryId=x", "", http.StatusBadRequest},
		"list wrong method":               {http.MethodPost, "/v1/actions", `{}`, http.StatusMethodNotAllowed},
		"run wrong method":                {http.MethodGet, "/v1/actions/run", "", http.StatusMethodNotAllowed},
		"run unknown field":               {http.MethodPost, "/v1/actions/run", `{"schemaVersion":1,"requestId":"r","idempotencyKey":"k","repositoryId":"repository_01","actionId":"tidy","command":"rm -rf"}`, http.StatusBadRequest},
		"run mismatched confirmation":     {http.MethodPost, "/v1/actions/run", `{"schemaVersion":1,"requestId":"r","idempotencyKey":"k","repositoryId":"repository_01","actionId":"tidy","confirmedActionId":"other"}`, http.StatusBadRequest},
		"run worktree and environment":    {http.MethodPost, "/v1/actions/run", `{"schemaVersion":1,"requestId":"r","idempotencyKey":"k","repositoryId":"repository_01","actionId":"tidy","worktreeId":"w","environmentId":"e"}`, http.StatusBadRequest},
		"run service without environment": {http.MethodPost, "/v1/actions/run", `{"schemaVersion":1,"requestId":"r","idempotencyKey":"k","repositoryId":"repository_01","actionId":"tidy","serviceId":"s"}`, http.StatusBadRequest},
		"run wrong schema":                {http.MethodPost, "/v1/actions/run", `{"schemaVersion":2,"requestId":"r","idempotencyKey":"k","repositoryId":"repository_01","actionId":"tidy"}`, http.StatusBadRequest},
		"unknown action route":            {http.MethodPost, "/v1/actions/tidy/run", `{}`, http.StatusNotFound},
	}
	for name, c := range cases {
		response := serveProfileActions(handler, c.method, c.path, c.body)
		if response.Code != c.status {
			t.Fatalf("%s: status=%d body=%s", name, response.Code, response.Body.String())
		}
	}
	if len(backend.runs) != 0 {
		t.Fatalf("invalid requests reached the backend: %+v", backend.runs)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/actions", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: status=%d", response.Code)
	}
	unavailable := profileActionsHandler(t, nil)
	if response := serveProfileActions(unavailable, http.MethodGet, "/v1/actions", ""); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable list: status=%d", response.Code)
	}
}
