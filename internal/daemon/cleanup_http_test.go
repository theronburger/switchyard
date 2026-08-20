package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

type cleanupHTTPBackend struct{}

func (cleanupHTTPBackend) Plan(context.Context, contractv2.CleanupPlanRequest) (contractv2.CleanupPlan, error) {
	now := time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC)
	return contractv2.CleanupPlan{
		SchemaVersion: contractv2.SchemaVersion, ID: "cleanup_plan_01", Revision: 1,
		Scope: contractv2.CleanupScope{Kind: "global"}, Candidates: []contractv2.CleanupCandidate{},
		Protected: []contractv2.CleanupProtection{}, CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}, nil
}

func (cleanupHTTPBackend) Apply(context.Context, contractv2.CleanupApplyRequest) (contractv2.CleanupResult, error) {
	return contractv2.CleanupResult{
		SchemaVersion: contractv2.SchemaVersion, PlanID: "cleanup_plan_01", PlanRevision: 1,
		Removals: []contractv2.CleanupRemoval{}, ClaimedAt: time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC),
		Attempts: 1, CompletedAt: time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC),
	}, nil
}

func TestCleanupHTTPBindsApplyBodyToPath(t *testing.T) {
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: "secret", DaemonInstanceID: "daemon_01", DaemonVersion: "test", StartedAt: time.Now(),
		StatusSource: staticStatusSource{snapshot: validHTTPStatus()}, Cleanup: cleanupHTTPBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/cleanup/plans/different/apply", strings.NewReader(`{"schemaVersion":2,"planId":"cleanup_plan_01","expectedRevision":1,"candidateIds":[]}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set(contractv2.SchemaVersionHeader, "2")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "INVALID_CLEANUP_REQUEST") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCleanupHTTPRejectsUnknownPlanFields(t *testing.T) {
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: "secret", DaemonInstanceID: "daemon_01", DaemonVersion: "test", StartedAt: time.Now(),
		StatusSource: staticStatusSource{snapshot: validHTTPStatus()}, Cleanup: cleanupHTTPBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/cleanup/plans", strings.NewReader(`{"schemaVersion":2,"scope":{"kind":"global"},"extra":true}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set(contractv2.SchemaVersionHeader, "2")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type cleanupHTTPErrorBackend struct {
	cleanupHTTPBackend
	err error
}

func (backend cleanupHTTPErrorBackend) Apply(context.Context, contractv2.CleanupApplyRequest) (contractv2.CleanupResult, error) {
	return contractv2.CleanupResult{}, backend.err
}

func TestCleanupHTTPPublishesClaimConflictsAndInterruption(t *testing.T) {
	cases := []struct {
		err       error
		status    int
		code      string
		retryable string
	}{
		{ErrCleanupApplyInProgress, http.StatusConflict, "CLEANUP_APPLY_IN_PROGRESS", `"retryable":true`},
		{state.ErrCleanupApplyMismatch, http.StatusConflict, "CLEANUP_APPLY_MISMATCH", `"retryable":false`},
		{state.ErrCleanupPlanConsumed, http.StatusConflict, "CLEANUP_PLAN_CONSUMED", `"retryable":false`},
		{context.Canceled, http.StatusServiceUnavailable, "CLEANUP_INTERRUPTED", `"retryable":true`},
	}
	for _, testCase := range cases {
		handler, err := NewHTTPHandler(HandlerConfig{
			Token: "secret", DaemonInstanceID: "daemon_01", DaemonVersion: "test", StartedAt: time.Now(),
			StatusSource: staticStatusSource{snapshot: validHTTPStatus()}, Cleanup: cleanupHTTPErrorBackend{err: testCase.err},
		})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/cleanup/plans/cleanup_plan_01/apply", strings.NewReader(`{"schemaVersion":2,"planId":"cleanup_plan_01","expectedRevision":1,"candidateIds":[]}`))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != testCase.status || !strings.Contains(response.Body.String(), testCase.code) || !strings.Contains(response.Body.String(), testCase.retryable) {
			t.Fatalf("%v: status=%d body=%s", testCase.err, response.Code, response.Body.String())
		}
	}
}
