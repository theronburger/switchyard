package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

type cleanupHTTPBackend struct{}

func (cleanupHTTPBackend) Plan(context.Context, contractv1.CleanupPlanRequest) (contractv1.CleanupPlan, error) {
	now := time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC)
	return contractv1.CleanupPlan{
		SchemaVersion: contractv1.SchemaVersion, ID: "cleanup_plan_01", Revision: 1,
		Scope: contractv1.CleanupScope{Kind: "global"}, Candidates: []contractv1.CleanupCandidate{},
		Protected: []contractv1.CleanupProtection{}, CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}, nil
}

func (cleanupHTTPBackend) Apply(context.Context, contractv1.CleanupApplyRequest) (contractv1.CleanupResult, error) {
	return contractv1.CleanupResult{
		SchemaVersion: contractv1.SchemaVersion, PlanID: "cleanup_plan_01", PlanRevision: 1,
		Removals: []contractv1.CleanupRemoval{}, CompletedAt: time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC),
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
	request := httptest.NewRequest(http.MethodPost, "/v1/cleanup/plans/different/apply", strings.NewReader(`{"schemaVersion":1,"planId":"cleanup_plan_01","expectedRevision":1,"candidateIds":[]}`))
	request.Header.Set("Authorization", "Bearer secret")
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
	request := httptest.NewRequest(http.MethodPost, "/v1/cleanup/plans", strings.NewReader(`{"schemaVersion":1,"scope":{"kind":"global"},"extra":true}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
