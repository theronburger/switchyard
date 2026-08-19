package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/integrations/githubstatus"
	"github.com/theronburger/switchyard/internal/state"
)

type fakeGitHubStatusClient struct {
	mutex        sync.Mutex
	account      string
	authError    error
	pullRequest  *contractv1.PullRequest
	found        bool
	queryError   error
	authCalls    int
	queryCalls   int
	repositories []string
	branches     []string
}

func (client *fakeGitHubStatusClient) ActiveAccount(context.Context) (string, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	client.authCalls++
	return client.account, client.authError
}

func (client *fakeGitHubStatusClient) PullRequest(
	_ context.Context,
	repository string,
	branch string,
) (*contractv1.PullRequest, bool, error) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	client.queryCalls++
	client.repositories = append(client.repositories, repository)
	client.branches = append(client.branches, branch)
	return client.pullRequest, client.found, client.queryError
}

func TestPullRequestCadenceStepsDownWithActivity(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	pullRequest := testPullRequest(now.Add(-5 * time.Minute))
	observation := &contractv1.PullRequestObservation{Status: "found", PullRequest: &pullRequest}

	tests := []struct {
		name     string
		mutate   func()
		expected time.Duration
	}{
		{name: "fresh", expected: time.Minute},
		{name: "two hours", mutate: func() { pullRequest.UpdatedAt = now.Add(-2 * time.Hour) }, expected: 3 * time.Minute},
		{name: "one day", mutate: func() { pullRequest.UpdatedAt = now.Add(-24 * time.Hour) }, expected: 15 * time.Minute},
		{name: "one week", mutate: func() { pullRequest.UpdatedAt = now.Add(-7 * 24 * time.Hour) }, expected: time.Hour},
		{name: "inactive", mutate: func() { pullRequest.UpdatedAt = now.Add(-8 * 24 * time.Hour) }, expected: 6 * time.Hour},
		{name: "pending stays hot", mutate: func() { pullRequest.Checks.State = "pending" }, expected: 30 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pullRequest = testPullRequest(now.Add(-5 * time.Minute))
			observation.PullRequest = &pullRequest
			if test.mutate != nil {
				test.mutate()
			}
			if got := pullRequestCadence(observation, now); got != test.expected {
				t.Fatalf("cadence: got %s, want %s", got, test.expected)
			}
		})
	}
}

func TestPullRequestObservationDueImmediatelyAfterLocalHeadChanges(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	pullRequest := testPullRequest(now.Add(-30 * 24 * time.Hour))
	worktree := contractv1.Worktree{
		HeadRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PullRequest: &contractv1.PullRequestObservation{
			Status: "found", LastAttemptAt: now.Add(-time.Minute), PullRequest: &pullRequest,
		},
	}
	if !pullRequestObservationDue(worktree, now) {
		t.Fatal("changed local head did not bypass inactive cadence")
	}
}

func TestGitHubObserverPublishesFoundPullRequest(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	store := openGitHubObserverStore(t, now)
	pullRequest := testPullRequest(now.Add(-time.Minute))
	client := &fakeGitHubStatusClient{account: "theronburger", pullRequest: &pullRequest, found: true}
	observer := &githubStatusObserver{store: store, client: client, now: func() time.Time { return now }}

	if err := observer.refreshDue(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := store.ReadSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observation := stored.Repositories[0].Worktrees[0].PullRequest
	if observation == nil || observation.Status != "found" || observation.Account != "theronburger" ||
		observation.PullRequest == nil || observation.PullRequest.Number != 830 || observation.Stale {
		t.Fatalf("unexpected observation: %#v", observation)
	}
	if client.authCalls != 1 || client.queryCalls != 1 || client.repositories[0] != "example/marketplace" {
		t.Fatalf("unexpected client calls: %#v", client)
	}
}

func TestGitHubObserverPreservesLastKnownStateOnFailure(t *testing.T) {
	ctx := context.Background()
	firstAttempt := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	now := firstAttempt.Add(10 * time.Minute)
	store := openGitHubObserverStore(t, firstAttempt)
	pullRequest := testPullRequest(firstAttempt.Add(-time.Hour))
	snapshot, _ := store.ReadSnapshot(ctx)
	snapshot.Repositories[0].Worktrees[0].PullRequest = &contractv1.PullRequestObservation{
		Status: "found", Account: "theronburger", ObservedAt: &firstAttempt, LastAttemptAt: firstAttempt,
		PullRequest: &pullRequest,
	}
	if _, err := store.CommitSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitHubStatusClient{account: "theronburger", queryError: githubstatus.ErrQueryFailed}
	observer := &githubStatusObserver{store: store, client: client, now: func() time.Time { return now }}

	if err := observer.refreshDue(ctx); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.ReadSnapshot(ctx)
	observation := stored.Repositories[0].Worktrees[0].PullRequest
	if observation == nil || !observation.Stale || observation.ErrorCode != "github_query_failed" ||
		observation.PullRequest == nil || observation.PullRequest.Number != 830 ||
		observation.ObservedAt == nil || !observation.ObservedAt.Equal(firstAttempt) {
		t.Fatalf("last-known state was not preserved: %#v", observation)
	}
}

func TestGitHubObserverAuthFailureDoesNotQueryBranches(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	store := openGitHubObserverStore(t, now)
	client := &fakeGitHubStatusClient{authError: githubstatus.ErrAuthenticationUnavailable}
	observer := &githubStatusObserver{store: store, client: client, now: func() time.Time { return now }}
	if err := observer.refreshDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.ReadSnapshot(context.Background())
	observation := stored.Repositories[0].Worktrees[0].PullRequest
	if client.queryCalls != 0 || observation == nil || observation.Status != "unavailable" ||
		observation.ErrorCode != "github_auth_unavailable" {
		t.Fatalf("unexpected auth failure state: calls=%d observation=%#v", client.queryCalls, observation)
	}
}

func openGitHubObserverStore(t *testing.T, now time.Time) *state.Store {
	t.Helper()
	store, err := state.Open(context.Background(), state.Config{
		Path: filepath.Join(t.TempDir(), "state.sqlite"), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CommitSnapshot(context.Background(), githubObserverSnapshot()); err != nil {
		t.Fatal(err)
	}
	return store
}

func githubObserverSnapshot() contractv1.StatusSnapshot {
	return contractv1.StatusSnapshot{
		Daemon: contractv1.DaemonStatus{
			InstanceID: "daemon_test", Version: "test", State: "ready",
			StartedAt: time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC),
		},
		Repositories: []contractv1.Repository{{
			ID: "repository_test", DisplayName: "marketplace", RootPath: "/repo",
			Adapter: "marketplace", Remote: "example/marketplace",
			Worktrees: []contractv1.Worktree{{
				ID: "worktree_test", Path: "/repo", Branch: "PROJ-830/imports",
				HeadRevision: "0123456789abcdef0123456789abcdef01234567", IsPrimary: true,
			}},
		}},
		Environments: []contractv1.Environment{}, Operations: []contractv1.Operation{}, Alerts: []contractv1.Alert{},
	}
}

func testPullRequest(updatedAt time.Time) contractv1.PullRequest {
	completedAt := updatedAt
	return contractv1.PullRequest{
		Number: 830, Title: "Chapter imports", URL: "https://github.com/example/marketplace/pull/830",
		State: "open", Mergeable: "mergeable", MergeState: "clean", ReviewDecision: "approved",
		BaseBranch: "main", HeadBranch: "PROJ-830/imports",
		HeadRevision: "0123456789abcdef0123456789abcdef01234567",
		CreatedAt:    updatedAt.Add(-time.Hour), UpdatedAt: updatedAt,
		Checks: contractv1.PullRequestChecks{
			State: "passing", Total: 1, Passing: 1,
			Items: []contractv1.PullRequestCheck{{
				Name: "CI", State: "success", Bucket: "pass", CompletedAt: &completedAt,
			}},
		},
	}
}
