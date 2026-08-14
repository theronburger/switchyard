package apiclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

func TestClientStartsAndStopsEnvironmentAfterHandshake(t *testing.T) {
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	acceptedAt := now.Add(time.Minute)
	var handshakes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization header was not sent")
		}
		if request.Header.Get("Origin") != "" {
			t.Errorf("native mutation client sent Origin")
		}
		switch request.URL.Path {
		case "/handshake":
			handshakes.Add(1)
			writeTestHandshake(t, response, snapshot)
		case "/v1/environments":
			if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
				t.Errorf("invalid start request method or content type")
			}
			var mutation contractv1.StartEnvironmentRequest
			decodeTestRequest(t, request, &mutation)
			if mutation.Validate() != nil || mutation.WorktreeID != "worktree_01" || len(mutation.ServiceIDs) != 2 {
				t.Errorf("start mutation: %+v", mutation)
			}
			response.WriteHeader(http.StatusAccepted)
			writeTestJSON(t, response, validClientReceipt(mutation.RequestID, "environment_01", acceptedAt))
		case "/v1/environments/environment_01/stop":
			var mutation contractv1.StopEnvironmentRequest
			decodeTestRequest(t, request, &mutation)
			if mutation.Validate() != nil || mutation.ExpectedEnvironmentRevision == nil ||
				*mutation.ExpectedEnvironmentRevision != 7 {
				t.Errorf("stop mutation: %+v", mutation)
			}
			response.WriteHeader(http.StatusAccepted)
			writeTestJSON(t, response, validClientReceipt(mutation.RequestID, "environment_01", acceptedAt))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	start, err := client.StartEnvironment(context.Background(), contractv1.StartEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request_start", IdempotencyKey: "start:key",
		},
		WorktreeID: "worktree_01", ServiceIDs: []string{"organizer", "nonprofit-service"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if start.EnvironmentID != "environment_01" || start.OperationID == "" {
		t.Fatalf("start receipt: %+v", start)
	}
	revision := int64(7)
	stop, err := client.StopEnvironment(context.Background(), "environment_01", contractv1.StopEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request_stop", IdempotencyKey: "stop:key",
			ExpectedEnvironmentRevision: &revision,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stop.EnvironmentID != "environment_01" || handshakes.Load() != 2 {
		t.Fatalf("stop receipt=%+v handshakes=%d", stop, handshakes.Load())
	}
}

func TestClientRejectsInvalidMutationBeforeTransport(t *testing.T) {
	var calls atomic.Int32
	client := NewClient(Connection{}, ClientOptions{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, nil
	})})
	_, err := client.StartEnvironment(context.Background(), contractv1.StartEnvironmentRequest{})
	if CodeOf(err) != ErrorActionRequestInvalid {
		t.Fatalf("start error: got %q", CodeOf(err))
	}
	_, err = client.StopEnvironment(context.Background(), "bad/id", contractv1.StopEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request", IdempotencyKey: "key",
		},
	})
	if CodeOf(err) != ErrorActionRequestInvalid {
		t.Fatalf("stop error: got %q", CodeOf(err))
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid mutation made %d transport calls", calls.Load())
	}
}

func TestClientReturnsStableMutationErrorWithoutPrivateBody(t *testing.T) {
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	privateDetail := "/Users/person/state.sqlite bearer-secret"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		if request.URL.Path == "/handshake" {
			writeTestHandshake(t, response, snapshot)
			return
		}
		response.WriteHeader(http.StatusConflict)
		writeTestJSON(t, response, map[string]any{
			"schemaVersion": contractv1.SchemaVersion,
			"error": map[string]any{
				"code": "ENVIRONMENT_BUSY", "message": privateDetail, "retryable": true,
			},
		})
	}))
	defer server.Close()
	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	_, err := client.StartEnvironment(context.Background(), validStartMutation())
	if CodeOf(err) != ErrorCode("ENVIRONMENT_BUSY") {
		t.Fatalf("error code: got %q", CodeOf(err))
	}
	if strings.Contains(err.Error(), "Users") || strings.Contains(err.Error(), "sqlite") ||
		strings.Contains(err.Error(), "bearer-secret") {
		t.Fatalf("mutation error leaked response detail: %v", err)
	}
}

func TestClientRejectsInvalidMutationReceipt(t *testing.T) {
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		if request.URL.Path == "/handshake" {
			writeTestHandshake(t, response, snapshot)
			return
		}
		response.WriteHeader(http.StatusAccepted)
		writeTestJSON(t, response, map[string]any{"schemaVersion": contractv1.SchemaVersion})
	}))
	defer server.Close()
	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	_, err := client.StartEnvironment(context.Background(), validStartMutation())
	if CodeOf(err) != ErrorDaemonResponseInvalid {
		t.Fatalf("error code: got %q", CodeOf(err))
	}
}

func validStartMutation() contractv1.StartEnvironmentRequest {
	return contractv1.StartEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request", IdempotencyKey: "key",
		},
		WorktreeID: "worktree", ServiceIDs: []string{"organizer"},
	}
}

func validClientReceipt(requestID, environmentID string, acceptedAt time.Time) contractv1.MutationReceipt {
	return contractv1.MutationReceipt{
		SchemaVersion: contractv1.SchemaVersion,
		RequestID:     requestID, OperationID: "operation_01", AcceptedAt: acceptedAt, EnvironmentID: environmentID,
	}
}

func writeTestHandshake(t *testing.T, response http.ResponseWriter, snapshot contractv1.StatusSnapshot) {
	t.Helper()
	writeTestJSON(t, response, Handshake{
		SchemaVersion: RuntimeDescriptorSchemaVersion, DaemonInstanceID: snapshot.Daemon.InstanceID,
		DaemonVersion: snapshot.Daemon.Version, SupportedSchemaVersions: []int{contractv1.SchemaVersion},
	})
}

func decodeTestRequest(t *testing.T, request *http.Request, destination any) {
	t.Helper()
	defer request.Body.Close()
	if err := json.NewDecoder(request.Body).Decode(destination); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
