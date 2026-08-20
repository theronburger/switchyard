package main

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/state"
)

const repositoryObserverSweep = 30 * time.Second

type repositoryObserverStore interface {
	UpdateSnapshot(context.Context, state.SnapshotUpdater) (contractv1.StatusSnapshot, bool, error)
}

type repositoryObserver struct {
	store    repositoryObserverStore
	paths    applicationPaths
	interval time.Duration
	now      func() time.Time
	discover func(context.Context, time.Time) repositoryInventory
	annotate func(applicationPaths, *repositoryInventory) error
	restore  func(context.Context, *repositoryInventory) error
	restart  func()
}

func newRepositoryObserver(store *state.Store, paths applicationPaths, restart func()) *repositoryObserver {
	return &repositoryObserver{
		store: store, paths: paths, interval: repositoryObserverSweep, now: time.Now,
		discover: func(ctx context.Context, observedAt time.Time) repositoryInventory {
			discovered, err := discoverAcceptedRepositoryInventory(ctx, store, observedAt)
			if err != nil {
				return inventoryFailure(observedAt, "CONFIGURATION_UNAVAILABLE", "Accepted configuration is unavailable.")
			}
			return discovered
		},
		annotate: annotateWorkspaceInventory,
		restore: func(ctx context.Context, inventory *repositoryInventory) error {
			return restoreWorkspaceInventory(ctx, store, inventory)
		},
		restart: restart,
	}
}

func (observer *repositoryObserver) Run(ctx context.Context) error {
	if observer == nil || observer.store == nil || observer.discover == nil ||
		observer.annotate == nil || observer.restore == nil || observer.now == nil || observer.interval <= 0 {
		return errors.New("repository observer is not configured")
	}
	ticker := time.NewTicker(observer.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := observer.RefreshOnce(ctx); err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
}

func (observer *repositoryObserver) RefreshOnce(ctx context.Context) error {
	attemptedAt := observer.now().UTC()
	discovered := observer.discover(ctx, attemptedAt)
	if discovered.AttemptedAt.IsZero() {
		discovered.AttemptedAt = attemptedAt
	}
	if err := observer.annotate(observer.paths, &discovered); err != nil {
		markInventoryRefreshFailed(
			&discovered, attemptedAt, "WORKSPACE_INVENTORY_UNAVAILABLE",
			"Workspace inventory could not be reconciled.",
		)
	}
	if err := observer.restore(ctx, &discovered); err != nil {
		markInventoryRefreshFailed(
			&discovered, attemptedAt, "WORKSPACE_STATE_UNAVAILABLE",
			"Persisted workspace state could not be reconciled.",
		)
	}
	topologyChanged := false
	_, _, err := observer.store.UpdateSnapshot(ctx, func(snapshot *contractv1.StatusSnapshot) (bool, error) {
		previousTopology := repositoryTopology(snapshot.Repositories)
		canRestart := inventoryContainsEnvironmentWorktrees(discovered.Repositories, snapshot.Environments)
		*snapshot = mergeRepositoryInventory(*snapshot, discovered)
		topologyChanged = canRestart &&
			!reflect.DeepEqual(previousTopology, repositoryTopology(snapshot.Repositories))
		return true, nil
	})
	if err == nil && topologyChanged && observer.restart != nil {
		observer.restart()
	}
	return err
}

func inventoryContainsEnvironmentWorktrees(
	repositories []contractv1.Repository,
	environments []contractv1.Environment,
) bool {
	for _, environment := range environments {
		found := false
		for _, repository := range repositories {
			if repository.ID == environment.RepositoryID && repositoryContainsWorktree(repository, environment.WorktreeID) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func markInventoryRefreshFailed(
	inventory *repositoryInventory,
	attemptedAt time.Time,
	code string,
	summary string,
) {
	inventory.Complete = false
	inventory.AttemptedAt = attemptedAt.UTC()
	inventory.Alerts = append(inventory.Alerts, newInventoryAlert(attemptedAt, code, summary, "error"))
	inventory.Alerts = deduplicateInventoryAlerts(inventory.Alerts)
	sort.Slice(inventory.Alerts, func(left, right int) bool {
		return inventory.Alerts[left].ID < inventory.Alerts[right].ID
	})
}

func repositoryTopology(repositories []contractv1.Repository) []string {
	topology := make([]string, 0)
	for _, repository := range repositories {
		topology = append(topology, "repository:"+repository.ID)
		for _, worktree := range repository.Worktrees {
			topology = append(topology, "worktree:"+repository.ID+":"+worktree.ID)
		}
	}
	sort.Strings(topology)
	return topology
}
