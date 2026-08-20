package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

type actionBackend struct {
	start func(context.Context, contractv2.StartEnvironmentRequest) (contractv2.MutationReceipt, error)
	stop  func(context.Context, string, contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error)
}

type workspaceActionBackend struct {
	create  func(context.Context, contractv2.CreateWorktreeRequest) (contractv2.MutationReceipt, error)
	adopt   func(context.Context, contractv2.AdoptWorktreeRequest) (contractv2.MutationReceipt, error)
	archive func(context.Context, contractv2.ArchiveWorktreeRequest) (contractv2.MutationReceipt, error)
	prepare func(context.Context, contractv2.PrepareWorktreeRequest) (contractv2.MutationReceipt, error)
}

func (backend workspaceActionBackend) CreateWorktree(
	ctx context.Context,
	request contractv2.CreateWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	return backend.create(ctx, request)
}

func (backend workspaceActionBackend) ArchiveWorktree(
	ctx context.Context,
	request contractv2.ArchiveWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	return backend.archive(ctx, request)
}

func (backend workspaceActionBackend) AdoptWorktree(
	ctx context.Context,
	request contractv2.AdoptWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	return backend.adopt(ctx, request)
}

func (backend workspaceActionBackend) PrepareWorktree(
	ctx context.Context,
	request contractv2.PrepareWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	return backend.prepare(ctx, request)
}

func TestWorkspaceMutationsReturnAcceptedReceiptsAndBindArchivePath(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 17, 15, 0, 0, 0, time.UTC)
	backend := workspaceActionBackend{
		create: func(_ context.Context, request contractv2.CreateWorktreeRequest) (contractv2.MutationReceipt, error) {
			if request.RepositoryID != "repository_01" || request.Branch != "feature/example" ||
				request.StartPoint != "origin/main" {
				t.Fatalf("create request: %+v", request)
			}
			return validMutationReceipt(request.RequestID, "", acceptedAt), nil
		},
		adopt: func(_ context.Context, request contractv2.AdoptWorktreeRequest) (contractv2.MutationReceipt, error) {
			if request.WorktreeID != "worktree_01" {
				t.Fatalf("adopt request: %+v", request)
			}
			return validMutationReceipt(request.RequestID, "", acceptedAt), nil
		},
		archive: func(_ context.Context, request contractv2.ArchiveWorktreeRequest) (contractv2.MutationReceipt, error) {
			if request.WorktreeID != "worktree_01" {
				t.Fatalf("archive request: %+v", request)
			}
			return validMutationReceipt(request.RequestID, "", acceptedAt), nil
		},
		prepare: func(_ context.Context, request contractv2.PrepareWorktreeRequest) (contractv2.MutationReceipt, error) {
			if request.WorktreeID != "worktree_01" {
				t.Fatalf("prepare request: %+v", request)
			}
			return validMutationReceipt(request.RequestID, "", acceptedAt), nil
		},
	}
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: testToken, DaemonInstanceID: "daemon_01", DaemonVersion: "0.1.0-dev",
		StartedAt:    time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC),
		StatusSource: staticStatusSource{snapshot: validHTTPStatus()}, WorkspaceActions: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	create := serveMutation(t, handler, "/v1/worktrees", map[string]any{
		"schemaVersion": contractv2.SchemaVersion, "requestId": "request_create",
		"idempotencyKey": "create:test", "repositoryId": "repository_01",
		"branch": "feature/example", "startPoint": "origin/main",
	})
	if create.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	adopt := serveMutation(t, handler, "/v1/worktrees/worktree_01/adopt", map[string]any{
		"schemaVersion": contractv2.SchemaVersion, "requestId": "request_adopt",
		"idempotencyKey": "adopt:test", "worktreeId": "worktree_01",
	})
	if adopt.Code != http.StatusAccepted {
		t.Fatalf("adopt status=%d body=%s", adopt.Code, adopt.Body.String())
	}
	archive := serveMutation(t, handler, "/v1/worktrees/worktree_01/archive", map[string]any{
		"schemaVersion": contractv2.SchemaVersion, "requestId": "request_archive",
		"idempotencyKey": "archive:test", "worktreeId": "worktree_01",
	})
	if archive.Code != http.StatusAccepted {
		t.Fatalf("archive status=%d body=%s", archive.Code, archive.Body.String())
	}
	prepare := serveMutation(t, handler, "/v1/worktrees/worktree_01/prepare", map[string]any{
		"schemaVersion": contractv2.SchemaVersion, "requestId": "request_prepare",
		"idempotencyKey": "prepare:test", "worktreeId": "worktree_01",
	})
	if prepare.Code != http.StatusAccepted {
		t.Fatalf("prepare status=%d body=%s", prepare.Code, prepare.Body.String())
	}
	mismatch := serveMutation(t, handler, "/v1/worktrees/worktree_01/archive", map[string]any{
		"schemaVersion": contractv2.SchemaVersion, "requestId": "request_mismatch",
		"idempotencyKey": "archive:mismatch", "worktreeId": "worktree_02",
	})
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("mismatched archive status=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	prepareMismatch := serveMutation(t, handler, "/v1/worktrees/worktree_01/prepare", map[string]any{
		"schemaVersion": contractv2.SchemaVersion, "requestId": "request_prepare_mismatch",
		"idempotencyKey": "prepare:mismatch", "worktreeId": "worktree_02",
	})
	if prepareMismatch.Code != http.StatusBadRequest {
		t.Fatalf("mismatched prepare status=%d body=%s", prepareMismatch.Code, prepareMismatch.Body.String())
	}
}

func (backend actionBackend) StartEnvironment(
	ctx context.Context,
	request contractv2.StartEnvironmentRequest,
) (contractv2.MutationReceipt, error) {
	return backend.start(ctx, request)
}

func (backend actionBackend) StopEnvironment(
	ctx context.Context,
	environmentID string,
	request contractv2.StopEnvironmentRequest,
) (contractv2.MutationReceipt, error) {
	return backend.stop(ctx, environmentID, request)
}

func TestEnvironmentMutationsReturnAcceptedReceipts(t *testing.T) {
	acceptedAt := time.Date(2026, 8, 14, 13, 30, 0, 0, time.UTC)
	backend := actionBackend{
		start: func(_ context.Context, request contractv2.StartEnvironmentRequest) (contractv2.MutationReceipt, error) {
			if request.WorktreeID != "worktree_test" || len(request.ServiceIDs) != 2 {
				t.Fatalf("start request: %+v", request)
			}
			return validMutationReceipt(request.RequestID, "environment_test", acceptedAt), nil
		},
		stop: func(_ context.Context, environmentID string, request contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error) {
			if environmentID != "environment_test" || request.ExpectedEnvironmentRevision == nil ||
				*request.ExpectedEnvironmentRevision != 3 {
				t.Fatalf("stop environment=%q request=%+v", environmentID, request)
			}
			return validMutationReceipt(request.RequestID, environmentID, acceptedAt), nil
		},
	}
	handler := newMutationTestHandler(t, backend)

	start := serveMutation(t, handler, "/v1/environments", map[string]any{
		"schemaVersion":  contractv2.SchemaVersion,
		"requestId":      "request_start",
		"idempotencyKey": "start:test",
		"worktreeId":     "worktree_test",
		"serviceIds":     []string{"organizer", "nonprofit-service"},
	})
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	assertMutationReceipt(t, start, "request_start", "environment_test")

	stop := serveMutation(t, handler, "/v1/environments/environment_test/stop", map[string]any{
		"schemaVersion":               contractv2.SchemaVersion,
		"requestId":                   "request_stop",
		"idempotencyKey":              "stop:test",
		"expectedEnvironmentRevision": 3,
	})
	if stop.Code != http.StatusAccepted {
		t.Fatalf("stop status=%d body=%s", stop.Code, stop.Body.String())
	}
	assertMutationReceipt(t, stop, "request_stop", "environment_test")
}

func TestEnvironmentMutationsRejectRequestsBeforeBackend(t *testing.T) {
	calls := 0
	backend := actionBackend{
		start: func(context.Context, contractv2.StartEnvironmentRequest) (contractv2.MutationReceipt, error) {
			calls++
			return contractv2.MutationReceipt{}, nil
		},
		stop: func(context.Context, string, contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error) {
			calls++
			return contractv2.MutationReceipt{}, nil
		},
	}
	handler := newMutationTestHandler(t, backend)
	valid := `{"schemaVersion":2,"requestId":"request","idempotencyKey":"key","worktreeId":"worktree","serviceIds":["organizer"]}`
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		status      int
	}{
		{name: "method", method: http.MethodGet, path: "/v1/environments", contentType: "application/json", body: valid, status: http.StatusMethodNotAllowed},
		{name: "content type", method: http.MethodPost, path: "/v1/environments", contentType: "text/plain", body: valid, status: http.StatusBadRequest},
		{name: "unknown field", method: http.MethodPost, path: "/v1/environments", contentType: "application/json", body: strings.TrimSuffix(valid, "}") + `,"surprise":true}`, status: http.StatusBadRequest},
		{name: "trailing value", method: http.MethodPost, path: "/v1/environments", contentType: "application/json", body: valid + `{}`, status: http.StatusBadRequest},
		{name: "null services", method: http.MethodPost, path: "/v1/environments", contentType: "application/json", body: `{"schemaVersion":2,"requestId":"request","idempotencyKey":"key","worktreeId":"worktree","serviceIds":null}`, status: http.StatusBadRequest},
		{name: "unsafe route", method: http.MethodPost, path: "/v1/environments/bad%20id/stop", contentType: "application/json", body: `{"schemaVersion":2,"requestId":"request","idempotencyKey":"key"}`, status: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := authenticatedRequest(test.method, test.path)
			request.Body = io.NopCloser(strings.NewReader(test.body))
			request.ContentLength = int64(len(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	oversized := authenticatedRequest(http.MethodPost, "/v1/environments")
	oversized.Header.Set("Content-Type", "application/json")
	oversized.ContentLength = maximumMutationBodyBytes + 1
	oversized.Body = io.NopCloser(strings.NewReader("{}"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, oversized)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized status=%d body=%s", response.Code, response.Body.String())
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached the action backend %d times", calls)
	}
}

func TestEnvironmentMutationsRequireAuthenticationAndRejectOrigin(t *testing.T) {
	calls := 0
	backend := actionBackend{
		start: func(context.Context, contractv2.StartEnvironmentRequest) (contractv2.MutationReceipt, error) {
			calls++
			return contractv2.MutationReceipt{}, nil
		},
		stop: func(context.Context, string, contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error) {
			calls++
			return contractv2.MutationReceipt{}, nil
		},
	}
	handler := newMutationTestHandler(t, backend)

	unauthenticated := httptest.NewRequest(http.MethodPost, "/v1/environments", strings.NewReader("{}"))
	unauthenticated.Header.Set("Content-Type", "application/json")
	unauthenticatedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", unauthenticatedResponse.Code)
	}

	origin := authenticatedRequest(http.MethodPost, "/v1/environments")
	origin.Header.Set("Origin", "null")
	originResponse := httptest.NewRecorder()
	handler.ServeHTTP(originResponse, origin)
	if originResponse.Code != http.StatusForbidden {
		t.Fatalf("origin status=%d", originResponse.Code)
	}
	if calls != 0 {
		t.Fatalf("rejected requests reached the action backend %d times", calls)
	}
}

func TestEnvironmentMutationErrorsAreStableAndRedacted(t *testing.T) {
	privateDetail := "private /Users/person/state.sqlite bearer-secret"
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name: "public conflict",
			err: &ActionError{Status: http.StatusConflict, Contract: contractv2.ContractError{
				Code: "ENVIRONMENT_BUSY", Message: "The environment already has an active operation.", Retryable: true,
				ResourceKind: "environment", ResourceID: "environment_01", Phase: "preparing-services",
				NextAction: "wait_for_active_operation",
			}},
			wantStatus: http.StatusConflict,
			wantCode:   "ENVIRONMENT_BUSY",
		},
		{name: "private error", err: errors.New(privateDetail), wantStatus: http.StatusInternalServerError, wantCode: "ACTION_FAILED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := actionBackend{
				start: func(context.Context, contractv2.StartEnvironmentRequest) (contractv2.MutationReceipt, error) {
					return contractv2.MutationReceipt{}, test.err
				},
				stop: func(context.Context, string, contractv2.StopEnvironmentRequest) (contractv2.MutationReceipt, error) {
					return contractv2.MutationReceipt{}, test.err
				},
			}
			response := serveMutation(t, newMutationTestHandler(t, backend), "/v1/environments", map[string]any{
				"schemaVersion": contractv2.SchemaVersion, "requestId": "request", "idempotencyKey": "key",
				"worktreeId": "worktree", "serviceIds": []string{"organizer"},
			})
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "Users") || strings.Contains(response.Body.String(), "sqlite") ||
				strings.Contains(response.Body.String(), "bearer-secret") {
				t.Fatalf("private failure leaked: %s", response.Body.String())
			}
			if test.name == "public conflict" {
				var failure errorResponse
				if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil ||
					failure.Error.ResourceID != "environment_01" || failure.Error.Phase != "preparing-services" ||
					failure.Error.NextAction != "wait_for_active_operation" {
					t.Fatalf("structured action error: %+v err=%v", failure, err)
				}
			}
		})
	}
}

func newMutationTestHandler(t *testing.T, actions EnvironmentActions) http.Handler {
	t.Helper()
	handler, err := NewHTTPHandler(HandlerConfig{
		Token:              testToken,
		DaemonInstanceID:   "daemon_01",
		DaemonVersion:      "0.1.0-dev",
		StartedAt:          time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC),
		StatusSource:       staticStatusSource{snapshot: validHTTPStatus()},
		EnvironmentActions: actions,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serveMutation(t *testing.T, handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	contents, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := authenticatedRequest(http.MethodPost, path)
	request.Body = io.NopCloser(strings.NewReader(string(contents)))
	request.ContentLength = int64(len(contents))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertMutationReceipt(t *testing.T, response *httptest.ResponseRecorder, requestID, environmentID string) {
	t.Helper()
	var receipt contractv2.MutationReceipt
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Validate() != nil || receipt.RequestID != requestID || receipt.EnvironmentID != environmentID {
		t.Fatalf("receipt: %+v", receipt)
	}
}

func validMutationReceipt(requestID, environmentID string, acceptedAt time.Time) contractv2.MutationReceipt {
	return contractv2.MutationReceipt{
		SchemaVersion: contractv2.SchemaVersion,
		RequestID:     requestID,
		OperationID:   "operation_test",
		AcceptedAt:    acceptedAt,
		EnvironmentID: environmentID,
	}
}
