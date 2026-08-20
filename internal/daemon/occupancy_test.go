package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

func newOccupancyTestHandler(t *testing.T) (http.Handler, *state.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := state.Open(ctx, state.Config{Path: filepath.Join(t.TempDir(), "state.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	snapshot := validHTTPStatus()
	snapshot.Repositories = []contractv2.Repository{{
		ID: "repo_01", DisplayName: "example", RootPath: "/tmp/repository", ProfileKey: "example",
		Worktrees: []contractv2.Worktree{{ID: "worktree_01", Path: "/tmp/worktree", HeadRevision: "abc"}},
	}}
	if _, err := store.CommitSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	committed, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(HandlerConfig{
		Token: testToken, DaemonInstanceID: committed.Daemon.InstanceID, DaemonVersion: committed.Daemon.Version,
		StartedAt: time.Now(), StatusSource: store,
		Occupancy: &OccupancyService{Store: store, NewID: func(prefix string) (string, error) { return prefix + "_fixed", nil }},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, store
}

func occupancyPost(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set(contractv2.SchemaVersionHeader, "2")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestOccupancyHTTPAcquiresPublishesAndReleasesLeases(t *testing.T) {
	handler, store := newOccupancyTestHandler(t)
	response := occupancyPost(handler, "/v1/worktrees/worktree_01/occupancy",
		`{"schemaVersion":2,"requestId":"request_01","worktreeId":"worktree_01","holderKind":"agent-task","holderLabel":"Agent task"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("acquire: %d %s", response.Code, response.Body.String())
	}
	var lease contractv2.OccupancyLease
	if err := json.Unmarshal(response.Body.Bytes(), &lease); err != nil {
		t.Fatal(err)
	}
	if lease.ID != "occupancy_fixed" || lease.State != "held" || lease.WorktreeID != "worktree_01" {
		t.Fatalf("lease: %+v", lease)
	}

	status := authenticatedRequest(http.MethodGet, "/v1/status")
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, status)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status: %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	var snapshot contractv2.StatusSnapshot
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Repositories[0].Worktrees[0].Occupancy; len(got) != 1 || got[0].ID != lease.ID {
		t.Fatalf("published occupancy: %+v", got)
	}

	// Repeating the same request is idempotent.
	repeat := occupancyPost(handler, "/v1/worktrees/worktree_01/occupancy",
		`{"schemaVersion":2,"requestId":"request_01","worktreeId":"worktree_01","holderKind":"agent-task","holderLabel":"Agent task"}`)
	if repeat.Code != http.StatusOK || !strings.Contains(repeat.Body.String(), `"id":"occupancy_fixed"`) {
		t.Fatalf("repeat: %d %s", repeat.Code, repeat.Body.String())
	}

	release := occupancyPost(handler, "/v1/worktrees/worktree_01/occupancy/occupancy_fixed/release",
		`{"schemaVersion":2,"requestId":"request_02","worktreeId":"worktree_01","leaseId":"occupancy_fixed"}`)
	if release.Code != http.StatusOK || !strings.Contains(release.Body.String(), `"state":"released"`) {
		t.Fatalf("release: %d %s", release.Code, release.Body.String())
	}
	held, err := store.ListHeldOccupancy(context.Background())
	if err != nil || len(held) != 0 {
		t.Fatalf("held after release: %+v %v", held, err)
	}
}

func TestOccupancyHTTPFailurePaths(t *testing.T) {
	handler, _ := newOccupancyTestHandler(t)
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name: "unknown worktree", path: "/v1/worktrees/worktree_missing/occupancy",
			body:       `{"schemaVersion":2,"requestId":"request_x","worktreeId":"worktree_missing","holderKind":"agent-task","holderLabel":"Agent task"}`,
			wantStatus: http.StatusNotFound, wantCode: "WORKTREE_NOT_FOUND",
		},
		{
			name: "body worktree differs from route", path: "/v1/worktrees/worktree_01/occupancy",
			body:       `{"schemaVersion":2,"requestId":"request_x","worktreeId":"worktree_02","holderKind":"agent-task","holderLabel":"Agent task"}`,
			wantStatus: http.StatusBadRequest, wantCode: "INVALID_OCCUPANCY_REQUEST",
		},
		{
			name: "holder kind is not a generic token", path: "/v1/worktrees/worktree_01/occupancy",
			body:       `{"schemaVersion":2,"requestId":"request_x","worktreeId":"worktree_01","holderKind":"Codex Desktop","holderLabel":"Agent task"}`,
			wantStatus: http.StatusBadRequest, wantCode: "INVALID_OCCUPANCY_REQUEST",
		},
		{
			name: "holder label carries a path", path: "/v1/worktrees/worktree_01/occupancy",
			body:       `{"schemaVersion":2,"requestId":"request_x","worktreeId":"worktree_01","holderKind":"agent-task","holderLabel":"/Users/someone/work"}`,
			wantStatus: http.StatusBadRequest, wantCode: "INVALID_OCCUPANCY_REQUEST",
		},
		{
			name: "unknown field", path: "/v1/worktrees/worktree_01/occupancy",
			body:       `{"schemaVersion":2,"requestId":"request_x","worktreeId":"worktree_01","holderKind":"agent-task","holderLabel":"Agent task","transcript":"..."}`,
			wantStatus: http.StatusBadRequest, wantCode: "INVALID_OCCUPANCY_REQUEST",
		},
		{
			name: "other contract generation", path: "/v1/worktrees/worktree_01/occupancy",
			body:       `{"schemaVersion":1,"requestId":"request_x","worktreeId":"worktree_01","holderKind":"agent-task","holderLabel":"Agent task"}`,
			wantStatus: http.StatusUpgradeRequired, wantCode: contractv2.UpgradeRequiredCode,
		},
		{
			name: "release unknown lease", path: "/v1/worktrees/worktree_01/occupancy/occupancy_missing/release",
			body:       `{"schemaVersion":2,"requestId":"request_x","worktreeId":"worktree_01","leaseId":"occupancy_missing"}`,
			wantStatus: http.StatusNotFound, wantCode: "OCCUPANCY_LEASE_NOT_FOUND",
		},
		{
			name: "release lease id differs from route", path: "/v1/worktrees/worktree_01/occupancy/occupancy_a/release",
			body:       `{"schemaVersion":2,"requestId":"request_x","worktreeId":"worktree_01","leaseId":"occupancy_b"}`,
			wantStatus: http.StatusBadRequest, wantCode: "INVALID_OCCUPANCY_REQUEST",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := occupancyPost(handler, test.path, test.body)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantCode) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	get := authenticatedRequest(http.MethodGet, "/v1/worktrees/worktree_01/occupancy")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, get)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET occupancy: %d", response.Code)
	}
}

func TestOccupancyHTTPWithoutServiceIsUnavailable(t *testing.T) {
	handler := newTestHandler(t, validHTTPStatus())
	response := occupancyPost(handler, "/v1/worktrees/worktree_01/occupancy",
		`{"schemaVersion":2,"requestId":"request_x","worktreeId":"worktree_01","holderKind":"agent-task","holderLabel":"Agent task"}`)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "OCCUPANCY_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
