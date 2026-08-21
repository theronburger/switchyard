package mcp

import (
	"fmt"
	"sort"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

const (
	maximumAttentionItems = 3
	maximumURLs           = 8
)

func BuildEnvironmentContext(
	snapshot contractv2.StatusSnapshot,
	environmentID string,
) (*contractv2.EnvironmentContext, error) {
	if environmentID == "" {
		return nil, nil
	}

	environment, found := environmentByID(snapshot.Environments, environmentID)
	if !found {
		return nil, fmt.Errorf("environment %q was not found", environmentID)
	}
	alerts := make(map[string]contractv2.Alert, len(snapshot.Alerts))
	for _, alert := range snapshot.Alerts {
		alerts[alert.ID] = alert
	}

	attention := make([]contractv2.AttentionItem, 0, min(len(environment.AttentionAlertIDs), maximumAttentionItems))
	for _, alertID := range environment.AttentionAlertIDs {
		alert, exists := alerts[alertID]
		if !exists {
			return nil, fmt.Errorf("environment %q references unknown alert", environmentID)
		}
		if len(attention) == maximumAttentionItems {
			continue
		}
		attention = append(attention, contractv2.AttentionItem{
			Severity: alert.Severity,
			Code:     alert.Code,
			Summary:  alert.Summary,
		})
	}

	urlNames := make([]string, 0, len(environment.URLs))
	for name := range environment.URLs {
		urlNames = append(urlNames, name)
	}
	sort.Strings(urlNames)
	urls := make(map[string]string, min(len(urlNames), maximumURLs))
	for _, name := range urlNames[:min(len(urlNames), maximumURLs)] {
		urls[name] = environment.URLs[name]
	}
	pullRequest := pullRequestContext(snapshot.Repositories, environment)
	runID := ""
	sourceRevision := ""
	sourceDirty := false
	for _, service := range environment.Services {
		if service.Run == nil {
			continue
		}
		if runID == "" {
			runID = service.Run.ID
			sourceRevision = service.Run.SourceRevision
			sourceDirty = service.Run.SourceHasTrackedChanges || service.Run.SourceHasUntrackedFiles
		}
	}

	return &contractv2.EnvironmentContext{
		Revision:       environment.Revision,
		EnvironmentID:  environment.ID,
		RunID:          runID,
		SourceRevision: sourceRevision,
		SourceDirty:    sourceDirty,
		DesiredState:   environment.DesiredState,
		ObservedState:  environment.ObservedState,
		Health:         environment.Health,
		URLs:           urls,
		AttentionCount: len(environment.AttentionAlertIDs),
		Attention:      attention,
		PullRequest:    pullRequest,
		Truncated: len(environment.AttentionAlertIDs) > maximumAttentionItems ||
			len(environment.URLs) > maximumURLs,
	}, nil
}

func pullRequestContext(
	repositories []contractv2.Repository,
	environment contractv2.Environment,
) *contractv2.PullRequestContext {
	for _, repository := range repositories {
		if repository.ID != environment.RepositoryID {
			continue
		}
		for _, worktree := range repository.Worktrees {
			if worktree.ID != environment.WorktreeID || worktree.PullRequest == nil ||
				worktree.PullRequest.Status != "found" || worktree.PullRequest.PullRequest == nil {
				continue
			}
			pullRequest := worktree.PullRequest.PullRequest
			return &contractv2.PullRequestContext{
				Number: pullRequest.Number, URL: pullRequest.URL, State: pullRequest.State,
				Draft: pullRequest.Draft, Mergeable: pullRequest.Mergeable,
				ReviewDecision: pullRequest.ReviewDecision, ChecksState: pullRequest.Checks.State,
				HeadMatchesLocal: pullRequest.HeadRevision == worktree.HeadRevision,
				Stale:            worktree.PullRequest.Stale,
			}
		}
	}
	return nil
}

func environmentByID(environments []contractv2.Environment, id string) (contractv2.Environment, bool) {
	for _, environment := range environments {
		if environment.ID == id {
			return environment, true
		}
	}
	return contractv2.Environment{}, false
}
