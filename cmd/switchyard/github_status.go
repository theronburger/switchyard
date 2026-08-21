package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/integrations/githubstatus"
	"github.com/theronburger/switchyard/internal/state"
)

const (
	githubExecutableOverride = "SWITCHYARD_GH_EXECUTABLE"
	githubObserverSweep      = 15 * time.Second
	githubQueryTimeout       = 45 * time.Second
	githubQueryConcurrency   = 3
	githubMaximumTargets     = 64
)

type githubStatusClient interface {
	ActiveAccount(context.Context) (string, error)
	PullRequest(context.Context, string, string) (*contractv2.PullRequest, bool, error)
}

type githubStatusStore interface {
	ReadSnapshot(context.Context) (contractv2.StatusSnapshot, error)
	UpdateSnapshot(context.Context, state.SnapshotUpdater) (contractv2.StatusSnapshot, bool, error)
}

type githubStatusObserver struct {
	store      githubStatusStore
	client     githubStatusClient
	setupError error
	now        func() time.Time
}

type githubStatusTarget struct {
	repositoryID string
	worktreeID   string
	repository   string
	branch       string
	headRevision string
	previous     *contractv2.PullRequestObservation
}

type githubStatusResult struct {
	target      githubStatusTarget
	account     string
	pullRequest *contractv2.PullRequest
	found       bool
	err         error
}

func newGitHubStatusObserver(store githubStatusStore) *githubStatusObserver {
	executable, err := githubstatus.ResolveExecutable(os.Getenv(githubExecutableOverride))
	if err != nil {
		return &githubStatusObserver{store: store, setupError: err, now: time.Now}
	}
	client, err := githubstatus.NewCLI(executable, githubstatus.OSRunner{}, "github.com")
	return &githubStatusObserver{store: store, client: client, setupError: err, now: time.Now}
}

func (observer *githubStatusObserver) Run(ctx context.Context) error {
	if observer == nil || observer.store == nil {
		return errors.New("github status observer is not configured")
	}
	if err := observer.refreshDue(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	ticker := time.NewTicker(githubObserverSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := observer.refreshDue(ctx); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

func (observer *githubStatusObserver) refreshDue(ctx context.Context) error {
	now := observer.now().UTC()
	snapshot, err := observer.store.ReadSnapshot(ctx)
	if err != nil {
		return err
	}
	targets := dueGitHubTargets(snapshot, now)
	if len(targets) == 0 {
		return nil
	}

	results := make([]githubStatusResult, 0, len(targets))
	if observer.setupError != nil || observer.client == nil {
		for _, target := range targets {
			results = append(results, githubStatusResult{target: target, err: observer.setupError})
		}
	} else {
		queryContext, cancel := context.WithTimeout(ctx, githubQueryTimeout)
		defer cancel()
		account, authError := observer.client.ActiveAccount(queryContext)
		if authError != nil {
			for _, target := range targets {
				results = append(results, githubStatusResult{target: target, err: authError})
			}
		} else {
			results = observer.queryTargets(queryContext, account, targets)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	_, _, err = observer.store.UpdateSnapshot(ctx, func(current *contractv2.StatusSnapshot) (bool, error) {
		changed := false
		for _, result := range results {
			worktree := currentWorktree(current, result.target)
			if worktree == nil {
				continue
			}
			next := pullRequestObservation(result, now)
			if !reflect.DeepEqual(worktree.PullRequest, next) {
				worktree.PullRequest = next
				changed = true
			}
		}
		return changed, nil
	})
	return err
}

func (observer *githubStatusObserver) queryTargets(
	ctx context.Context,
	account string,
	targets []githubStatusTarget,
) []githubStatusResult {
	results := make(chan githubStatusResult, len(targets))
	semaphore := make(chan struct{}, githubQueryConcurrency)
	var waitGroup sync.WaitGroup
	for _, target := range targets {
		target := target
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- githubStatusResult{target: target, account: account, err: ctx.Err()}
				return
			}
			pullRequest, found, err := observer.client.PullRequest(ctx, target.repository, target.branch)
			results <- githubStatusResult{
				target: target, account: account, pullRequest: pullRequest, found: found, err: err,
			}
		}()
	}
	waitGroup.Wait()
	close(results)
	collected := make([]githubStatusResult, 0, len(targets))
	for result := range results {
		collected = append(collected, result)
	}
	return collected
}

func dueGitHubTargets(snapshot contractv2.StatusSnapshot, now time.Time) []githubStatusTarget {
	targets := make([]githubStatusTarget, 0)
	for _, repository := range snapshot.Repositories {
		if !githubstatus.ValidRepository(repository.Remote) {
			continue
		}
		for index := range repository.Worktrees {
			if len(targets) == githubMaximumTargets {
				return targets
			}
			worktree := repository.Worktrees[index]
			if worktree.Branch == "" || !pullRequestObservationDue(worktree, now) {
				continue
			}
			targets = append(targets, githubStatusTarget{
				repositoryID: repository.ID, worktreeID: worktree.ID, repository: repository.Remote,
				branch: worktree.Branch, headRevision: worktree.HeadRevision, previous: worktree.PullRequest,
			})
		}
	}
	return targets
}

func pullRequestObservationDue(worktree contractv2.Worktree, now time.Time) bool {
	observation := worktree.PullRequest
	if observation == nil || observation.LastAttemptAt.IsZero() {
		return true
	}
	if observation.Status == "found" && observation.PullRequest != nil &&
		observation.PullRequest.HeadRevision != worktree.HeadRevision {
		return true
	}
	return !now.Before(observation.LastAttemptAt.Add(pullRequestCadence(observation, now)))
}

func pullRequestCadence(observation *contractv2.PullRequestObservation, now time.Time) time.Duration {
	if observation == nil {
		return 0
	}
	if observation.Status == "unavailable" || observation.Stale {
		return 5 * time.Minute
	}
	if observation.Status == "none" || observation.PullRequest == nil {
		return time.Hour
	}
	pullRequest := observation.PullRequest
	if pullRequest.Checks.State == "pending" {
		return 30 * time.Second
	}
	if pullRequest.State != "open" {
		return 6 * time.Hour
	}
	activityAge := now.Sub(pullRequest.UpdatedAt)
	switch {
	case activityAge <= 15*time.Minute:
		return time.Minute
	case activityAge <= 2*time.Hour:
		return 3 * time.Minute
	case activityAge <= 24*time.Hour:
		return 15 * time.Minute
	case activityAge <= 7*24*time.Hour:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}

func currentWorktree(
	snapshot *contractv2.StatusSnapshot,
	target githubStatusTarget,
) *contractv2.Worktree {
	for repositoryIndex := range snapshot.Repositories {
		repository := &snapshot.Repositories[repositoryIndex]
		if repository.ID != target.repositoryID || repository.Remote != target.repository {
			continue
		}
		for worktreeIndex := range repository.Worktrees {
			worktree := &repository.Worktrees[worktreeIndex]
			if worktree.ID == target.worktreeID && worktree.Branch == target.branch &&
				worktree.HeadRevision == target.headRevision {
				return worktree
			}
		}
	}
	return nil
}

func pullRequestObservation(result githubStatusResult, now time.Time) *contractv2.PullRequestObservation {
	if result.err == nil {
		observedAt := now
		status := "none"
		if result.found && result.pullRequest != nil {
			status = "found"
		}
		return &contractv2.PullRequestObservation{
			Status: status, Account: result.account, ObservedAt: &observedAt, LastAttemptAt: now,
			PullRequest: result.pullRequest,
		}
	}
	errorCode := githubstatus.ErrorCode(result.err)
	if result.target.previous != nil &&
		(result.target.previous.Status == "found" || result.target.previous.Status == "none") {
		preserved := *result.target.previous
		preserved.LastAttemptAt = now
		preserved.Stale = true
		preserved.ErrorCode = errorCode
		return &preserved
	}
	return &contractv2.PullRequestObservation{
		Status: "unavailable", Account: result.account, LastAttemptAt: now, ErrorCode: errorCode,
	}
}
