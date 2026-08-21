package apiclient

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

func TestClientMutatesRepositoryConfigurationAndSurfacesConflicts(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	token := testToken()
	snapshot := validSnapshot(now)
	digest := "sha256:" + strings.Repeat("a", 64)
	source := "sha256:" + strings.Repeat("b", 64)
	var received contractv2.ConfigurationRepositoryMutationRequest
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		secureJSONHeaders(response)
		switch request.URL.Path {
		case "/handshake":
			writeTestHandshake(t, response, snapshot)
		case "/v1/configuration/repositories":
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer "+token {
				t.Errorf("mutation request: %s", request.Method)
			}
			contents, _ := io.ReadAll(request.Body)
			if err := json.Unmarshal(contents, &received); err != nil {
				t.Errorf("mutation body: %v", err)
			}
			if received.Key == "stale" {
				response.WriteHeader(http.StatusConflict)
				writeTestJSON(t, response, map[string]any{
					"schemaVersion": contractv2.SchemaVersion,
					"error":         map[string]any{"code": "CONFIGURATION_DESIRED_CHANGED", "message": "changed", "retryable": true},
				})
				return
			}
			writeTestJSON(t, response, contractv2.ConfigurationStatus{
				SchemaVersion: contractv2.SchemaVersion, State: "pending", AcceptedRevision: 2, AcceptedDigest: digest,
				Candidate: &contractv2.ConfigurationCandidate{
					SchemaVersion: contractv2.SchemaVersion, Digest: source, SourceDigest: source, CompilerVersion: "compiler",
					RepositoryDigests: map[string]string{"sample": digest}, ExecutableDigests: map[string]string{}, StagedAt: now,
				},
				Desired: &contractv2.ConfigurationDesiredFile{
					Present: true, SourceDigest: source,
					Repositories: []contractv2.ConfigurationRepositoryEntry{*received.Entry},
				},
			})
		default:
			t.Errorf("unexpected path %s", request.URL.Path)
		}
	}))
	defer server.Close()
	client := NewClient(connectionForServer(t, server.URL, token, snapshot, now), ClientOptions{})
	entry := &contractv2.ConfigurationRepositoryEntry{
		Key: "sample", Enabled: true, DisplayName: "Sample", Root: "/tmp/sample",
		Remote: "origin", DefaultBase: "origin/main", ManagedWorktreesRoot: "/tmp/sample-worktrees",
	}
	status, err := client.MutateRepositoryConfiguration(context.Background(), contractv2.ConfigurationRepositoryMutationRequest{
		SchemaVersion: contractv2.SchemaVersion, ExpectedRevision: 2, ExpectedSourceDigest: source,
		Operation: contractv2.ConfigurationRepositoryUpsert, Key: "sample", Entry: entry,
	})
	if err != nil || status.State != "pending" || status.Desired == nil || len(status.Desired.Repositories) != 1 ||
		received.Operation != "upsert" || received.ExpectedSourceDigest != source {
		t.Fatalf("status=%+v err=%v received=%+v", status, err, received)
	}

	_, err = client.MutateRepositoryConfiguration(context.Background(), contractv2.ConfigurationRepositoryMutationRequest{
		SchemaVersion: contractv2.SchemaVersion, ExpectedRevision: 2, ExpectedSourceDigest: source,
		Operation: contractv2.ConfigurationRepositoryRemove, Key: "stale",
	})
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != "CONFIGURATION_DESIRED_CHANGED" {
		t.Fatalf("expected the daemon's conflict code, got %v", err)
	}

	_, err = client.MutateRepositoryConfiguration(context.Background(), contractv2.ConfigurationRepositoryMutationRequest{
		SchemaVersion: contractv2.SchemaVersion, Operation: "repoint", Key: "sample",
	})
	if !errors.As(err, &coded) || coded.Code != ErrorActionRequestInvalid {
		t.Fatalf("invalid request must be rejected locally, got %v", err)
	}
}
