package apiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

func TestClientAcquiresAndReleasesOccupancyThroughExactRoutes(t *testing.T) {
	now := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.Method+" "+request.URL.Path)
		secureJSONHeaders(response)
		switch request.URL.Path {
		case "/handshake":
			writeTestJSON(t, response, Handshake{
				SchemaVersion: contractv2.SchemaVersion, DaemonInstanceID: snapshot.Daemon.InstanceID,
				DaemonVersion: snapshot.Daemon.Version, SupportedSchemaVersions: []int{contractv2.SchemaVersion},
			})
		case "/v1/worktrees/worktree_01/occupancy":
			var body contractv2.AcquireOccupancyRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Validate() != nil {
				http.Error(response, "bad", http.StatusBadRequest)
				return
			}
			writeTestJSON(t, response, contractv2.OccupancyLease{
				ID: "occupancy_01", WorktreeID: "worktree_01", HolderKind: body.HolderKind, HolderLabel: body.HolderLabel,
				State: "held", AcquiredAt: now,
			})
		case "/v1/worktrees/worktree_01/occupancy/occupancy_01/release":
			released := now.Add(time.Minute)
			writeTestJSON(t, response, contractv2.OccupancyLease{
				ID: "occupancy_01", WorktreeID: "worktree_01", HolderKind: "agent-task", HolderLabel: "Agent task",
				State: "released", AcquiredAt: now, ReleasedAt: &released,
			})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})

	lease, err := client.AcquireOccupancy(context.Background(), contractv2.AcquireOccupancyRequest{
		SchemaVersion: contractv2.SchemaVersion, RequestID: "request_01", WorktreeID: "worktree_01",
		HolderKind: "agent-task", HolderLabel: "Agent task",
	})
	if err != nil || lease.ID != "occupancy_01" || lease.State != "held" {
		t.Fatalf("acquire: %+v %v", lease, err)
	}
	released, err := client.ReleaseOccupancy(context.Background(), contractv2.ReleaseOccupancyRequest{
		SchemaVersion: contractv2.SchemaVersion, RequestID: "request_02", WorktreeID: "worktree_01", LeaseID: "occupancy_01",
	})
	if err != nil || released.State != "released" {
		t.Fatalf("release: %+v %v", released, err)
	}
	want := []string{
		"GET /handshake", "POST /v1/worktrees/worktree_01/occupancy",
		"GET /handshake", "POST /v1/worktrees/worktree_01/occupancy/occupancy_01/release",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths: %v", paths)
	}
	for index := range want {
		if paths[index] != want[index] {
			t.Fatalf("paths: %v", paths)
		}
	}
}

func TestClientRejectsUnsafeOccupancyRequestsBeforeSending(t *testing.T) {
	now := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	snapshot := validSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unsafe request reached the daemon")
	}))
	defer server.Close()
	client := NewClient(connectionForServer(t, server.URL, testToken(), snapshot, now), ClientOptions{})
	requests := []contractv2.AcquireOccupancyRequest{
		{SchemaVersion: 1, RequestID: "request_01", WorktreeID: "worktree_01", HolderKind: "agent-task", HolderLabel: "Agent task"},
		{SchemaVersion: 2, RequestID: "request_01", WorktreeID: "worktree/../01", HolderKind: "agent-task", HolderLabel: "Agent task"},
		{SchemaVersion: 2, RequestID: "request_01", WorktreeID: "worktree_01", HolderKind: "Agent", HolderLabel: "Agent task"},
		{SchemaVersion: 2, RequestID: "request_01", WorktreeID: "worktree_01", HolderKind: "agent-task", HolderLabel: "/Users/x"},
	}
	for _, request := range requests {
		if _, err := client.AcquireOccupancy(context.Background(), request); CodeOf(err) != ErrorActionRequestInvalid {
			t.Fatalf("request %+v: %v", request, err)
		}
	}
	if _, err := client.ReleaseOccupancy(context.Background(), contractv2.ReleaseOccupancyRequest{
		SchemaVersion: 2, RequestID: "request_02", WorktreeID: "worktree_01", LeaseID: "lease?x=1",
	}); CodeOf(err) != ErrorActionRequestInvalid {
		t.Fatalf("unsafe lease id: %v", err)
	}
}
