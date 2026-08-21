package apiclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

func TestClientListsAndRunsProfileActions(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	digest := "sha256:" + strings.Repeat("a", 64)
	var runBody contractv2.RunProfileActionRequest
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		switch request.URL.Path {
		case "/handshake":
			writeTestHandshake(t, response, snapshot)
		case "/v1/actions":
			if request.Method != http.MethodGet || request.Header.Get("Authorization") != "Bearer "+token {
				t.Errorf("list request: %s", request.Method)
			}
			writeTestJSON(t, response, contractv2.ProfileActionList{
				SchemaVersion: contractv2.SchemaVersion, AcceptedDigest: digest,
				Actions: []contractv2.ProfileAction{{
					ID: "tidy", RepositoryID: "repository_01", ProfileKey: "sample", ProfileDigest: digest,
					DisplayName: "Tidy", Scope: "worktree", Risk: "local", Kind: "command",
				}},
			})
		case "/v1/actions/run":
			contents, _ := io.ReadAll(request.Body)
			if err := json.Unmarshal(contents, &runBody); err != nil {
				t.Errorf("run body: %v", err)
			}
			response.WriteHeader(http.StatusAccepted)
			writeTestJSON(t, response, contractv2.MutationReceipt{
				SchemaVersion: contractv2.SchemaVersion, RequestID: runBody.RequestID, OperationID: "operation_01", AcceptedAt: now,
			})
		default:
			t.Errorf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	list, err := client.ListProfileActions(context.Background())
	if err != nil || len(list.Actions) != 1 || list.Actions[0].ID != "tidy" {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	receipt, err := client.RunProfileAction(context.Background(), contractv2.RunProfileActionRequest{
		MutationRequest: contractv2.MutationRequest{SchemaVersion: contractv2.SchemaVersion, RequestID: "request_01", IdempotencyKey: "key_01"},
		RepositoryID:    "repository_01", ActionID: "tidy", WorktreeID: "worktree_01",
	})
	if err != nil || receipt.OperationID != "operation_01" || runBody.WorktreeID != "worktree_01" || runBody.ActionID != "tidy" {
		t.Fatalf("run: %+v err=%v body=%+v", receipt, err, runBody)
	}
	_, err = client.RunProfileAction(context.Background(), contractv2.RunProfileActionRequest{
		MutationRequest: contractv2.MutationRequest{SchemaVersion: contractv2.SchemaVersion, RequestID: "request_02", IdempotencyKey: "key_02"},
		RepositoryID:    "repository_01", ActionID: "tidy", ServiceID: "web",
	})
	if CodeOf(err) != ErrorActionRequestInvalid {
		t.Fatalf("invalid request reached the daemon: %v", err)
	}
}

func TestClientRejectsActionListOutsideTheFiniteVocabulary(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		if request.URL.Path == "/handshake" {
			writeTestHandshake(t, response, snapshot)
			return
		}
		_, _ = response.Write([]byte(`{"schemaVersion":2,"actions":[{"id":"tidy","repositoryId":"repository_01","profileKey":"sample","profileDigest":"sha256:` +
			strings.Repeat("a", 64) + `","displayName":"Tidy","scope":"worktree","risk":"destructive","kind":"command","requiresConfirmation":false}]}`))
	}))
	defer server.Close()
	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	if _, err := client.ListProfileActions(context.Background()); CodeOf(err) != ErrorDaemonResponseInvalid {
		t.Fatalf("invalid risk accepted: %v", err)
	}
}
