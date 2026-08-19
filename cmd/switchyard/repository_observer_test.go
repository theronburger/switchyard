package main

import (
	"context"
	"sync"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	"github.com/theronburger/switchyard/internal/state"
)

type fakeRepositoryObserverStore struct {
	mutex    sync.Mutex
	snapshot contractv1.StatusSnapshot
	updates  int
}

func (store *fakeRepositoryObserverStore) UpdateSnapshot(
	_ context.Context,
	update state.SnapshotUpdater,
) (contractv1.StatusSnapshot, bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	changed, err := update(&store.snapshot)
	if changed {
		store.updates++
	}
	return store.snapshot, changed, err
}

func TestRepositoryObserverRefreshesRepositoryWithoutOverwritingEnvironment(t *testing.T) {
	observedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	previous := inventoryTestRepository("repo_test", "worktree_test", "/tmp/marketplace")
	previous.Worktrees[0].HeadRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	latest := inventoryTestRepository("repo_test", "worktree_test", "/tmp/marketplace")
	latest.Worktrees[0].HeadRevision = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	latest.Worktrees[0].Git.HasTrackedChanges = true
	latest.Observation = &contractv1.RepositoryObservation{
		ObservedAt: &observedAt, LastAttemptAt: observedAt,
	}
	environment := contractv1.Environment{ID: "environment_test", Revision: 9}
	store := &fakeRepositoryObserverStore{snapshot: contractv1.StatusSnapshot{
		Repositories: []contractv1.Repository{previous}, Environments: []contractv1.Environment{environment},
		Alerts: []contractv1.Alert{},
	}}
	observer := &repositoryObserver{
		store: store, interval: time.Second, now: func() time.Time { return observedAt },
		discover: func(context.Context, time.Time) repositoryInventory {
			return repositoryInventory{
				Repositories: []contractv1.Repository{latest}, Alerts: []contractv1.Alert{},
				Complete: true, AttemptedAt: observedAt,
			}
		},
		annotate: func(applicationPaths, *repositoryInventory) error { return nil },
		restore:  func(context.Context, *repositoryInventory) error { return nil },
	}
	if err := observer.RefreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.snapshot.Repositories[0].Worktrees[0].HeadRevision != latest.Worktrees[0].HeadRevision ||
		!store.snapshot.Repositories[0].Worktrees[0].Git.HasTrackedChanges {
		t.Fatalf("repository was not refreshed: %#v", store.snapshot.Repositories[0])
	}
	if len(store.snapshot.Environments) != 1 || store.snapshot.Environments[0].Revision != 9 {
		t.Fatalf("environment was overwritten: %#v", store.snapshot.Environments)
	}
}

func TestFailedRepositoryRefreshPreservesDataAndMarksObservationStale(t *testing.T) {
	observedAt := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)
	attemptedAt := observedAt.Add(time.Minute)
	repository := inventoryTestRepository("repo_test", "worktree_test", "/tmp/marketplace")
	repository.Observation = &contractv1.RepositoryObservation{
		ObservedAt: &observedAt, LastAttemptAt: observedAt,
	}
	merged := mergeRepositoryInventory(contractv1.StatusSnapshot{
		Repositories: []contractv1.Repository{repository}, Alerts: []contractv1.Alert{},
	}, inventoryFailure(attemptedAt, "REPOSITORY_WORKTREES_UNAVAILABLE", "Worktrees unavailable."))
	got := merged.Repositories[0]
	if got.Worktrees[0].ID != repository.Worktrees[0].ID || got.Observation == nil ||
		!got.Observation.Stale || got.Observation.ErrorCode != "REPOSITORY_WORKTREES_UNAVAILABLE" ||
		!got.Observation.LastAttemptAt.Equal(attemptedAt) || got.Observation.ObservedAt == nil ||
		!got.Observation.ObservedAt.Equal(observedAt) {
		t.Fatalf("stale repository observation: %#v", got)
	}
}
