package mcp

import (
	"fmt"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

func TestBuildEnvironmentContextCapsAttentionAndURLsDeterministically(t *testing.T) {
	alerts := make([]contractv2.Alert, 0, 5)
	alertIDs := make([]string, 0, 5)
	for index := range 5 {
		id := fmt.Sprintf("alert_%d", index)
		alertIDs = append(alertIDs, id)
		alerts = append(alerts, contractv2.Alert{
			ID:            id,
			EnvironmentID: "env_test",
			Severity:      "error",
			Code:          fmt.Sprintf("CODE_%d", index),
			Summary:       fmt.Sprintf("Attention %d", index),
			Status:        "active",
			FirstSeenAt:   time.Now(),
			LastSeenAt:    time.Now(),
			Occurrences:   1,
		})
	}
	urls := make(map[string]string)
	for index := 9; index >= 0; index-- {
		name := fmt.Sprintf("service-%02d", index)
		urls[name] = "http://127.0.0.1:7000"
	}
	snapshot := contractv2.StatusSnapshot{
		Environments: []contractv2.Environment{{
			ID:                "env_test",
			Revision:          17,
			DesiredState:      "running",
			ObservedState:     "running",
			Health:            "degraded",
			URLs:              urls,
			AttentionAlertIDs: alertIDs,
		}},
		Alerts: alerts,
	}

	context, err := BuildEnvironmentContext(snapshot, "env_test")
	if err != nil {
		t.Fatal(err)
	}
	if context.AttentionCount != 5 || len(context.Attention) != maximumAttentionItems {
		t.Fatalf("attention: total=%d returned=%d", context.AttentionCount, len(context.Attention))
	}
	if context.Attention[2].Code != "CODE_2" {
		t.Fatalf("attention order: got %q", context.Attention[2].Code)
	}
	if len(context.URLs) != maximumURLs {
		t.Fatalf("urls: got %d, want %d", len(context.URLs), maximumURLs)
	}
	if _, exists := context.URLs["service-07"]; !exists {
		t.Fatal("deterministic URL prefix omitted service-07")
	}
	if _, exists := context.URLs["service-08"]; exists {
		t.Fatal("URL cap included service-08")
	}
	if !context.Truncated {
		t.Fatal("context did not report truncation")
	}
}

func TestBuildEnvironmentContextOmitsFooterForGlobalCalls(t *testing.T) {
	context, err := BuildEnvironmentContext(contractv2.StatusSnapshot{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if context != nil {
		t.Fatalf("global call returned footer: %+v", context)
	}
}

func TestBuildEnvironmentContextIncludesPullRequestStatus(t *testing.T) {
	snapshot := contractv2.StatusSnapshot{
		Repositories: []contractv2.Repository{{
			ID: "repo_test",
			Worktrees: []contractv2.Worktree{{
				ID: "worktree_test", HeadRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				PullRequest: &contractv2.PullRequestObservation{
					Status: "found", Stale: true,
					PullRequest: &contractv2.PullRequest{
						Number: 9556, URL: "https://github.com/example/sample/pull/42",
						State: "open", Mergeable: "mergeable", ReviewDecision: "review_required",
						HeadRevision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
						Checks:       contractv2.PullRequestChecks{State: "passing"},
					},
				},
			}},
		}},
		Environments: []contractv2.Environment{{
			ID: "env_test", Revision: 1, RepositoryID: "repo_test", WorktreeID: "worktree_test",
			URLs: map[string]string{}, AttentionAlertIDs: []string{},
		}},
		Alerts: []contractv2.Alert{},
	}
	context, err := BuildEnvironmentContext(snapshot, "env_test")
	if err != nil {
		t.Fatal(err)
	}
	if context.PullRequest == nil || context.PullRequest.Number != 9556 ||
		context.PullRequest.ChecksState != "passing" || context.PullRequest.HeadMatchesLocal ||
		!context.PullRequest.Stale {
		t.Fatalf("unexpected pull request context: %#v", context.PullRequest)
	}
}

func TestBuildEnvironmentContextRejectsUnknownEnvironmentAndAlert(t *testing.T) {
	if _, err := BuildEnvironmentContext(contractv2.StatusSnapshot{}, "env_missing"); err == nil {
		t.Fatal("expected unknown environment error")
	}
	snapshot := contractv2.StatusSnapshot{Environments: []contractv2.Environment{{
		ID:                "env_test",
		URLs:              map[string]string{},
		AttentionAlertIDs: []string{"alert_missing"},
	}}}
	if _, err := BuildEnvironmentContext(snapshot, "env_test"); err == nil {
		t.Fatal("expected unknown alert error")
	}
}
