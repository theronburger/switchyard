package main

import (
	"context"
	"sync"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/state"
)

type fakeRepositoryObserverStore struct {
	mutex    sync.Mutex
	snapshot contractv2.StatusSnapshot
	updates  int
}

func (store *fakeRepositoryObserverStore) UpdateSnapshot(
	_ context.Context,
	update state.SnapshotUpdater,
) (contractv2.StatusSnapshot, bool, error) {
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
	previous := inventoryTestRepository("repo_test", "worktree_test", "/tmp/sample")
	previous.Worktrees[0].HeadRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	latest := inventoryTestRepository("repo_test", "worktree_test", "/tmp/sample")
	latest.Worktrees[0].HeadRevision = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	latest.Worktrees[0].Git.HasTrackedChanges = true
	latest.Observation = &contractv2.RepositoryObservation{
		ObservedAt: &observedAt, LastAttemptAt: observedAt,
	}
	environment := contractv2.Environment{ID: "environment_test", Revision: 9}
	store := &fakeRepositoryObserverStore{snapshot: contractv2.StatusSnapshot{
		Repositories: []contractv2.Repository{previous}, Environments: []contractv2.Environment{environment},
		Alerts: []contractv2.Alert{},
	}}
	observer := &repositoryObserver{
		store: store, interval: time.Second, now: func() time.Time { return observedAt },
		discover: func(context.Context, time.Time) repositoryInventory {
			return repositoryInventory{
				Repositories: []contractv2.Repository{latest}, Alerts: []contractv2.Alert{},
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
	repository := inventoryTestRepository("repo_test", "worktree_test", "/tmp/sample")
	repository.Observation = &contractv2.RepositoryObservation{
		ObservedAt: &observedAt, LastAttemptAt: observedAt,
	}
	merged := mergeRepositoryInventory(contractv2.StatusSnapshot{
		Repositories: []contractv2.Repository{repository}, Alerts: []contractv2.Alert{},
	}, inventoryFailure(attemptedAt, "REPOSITORY_WORKTREES_UNAVAILABLE", "Worktrees unavailable."))
	got := merged.Repositories[0]
	if got.Worktrees[0].ID != repository.Worktrees[0].ID || got.Observation == nil ||
		!got.Observation.Stale || got.Observation.ErrorCode != "REPOSITORY_WORKTREES_UNAVAILABLE" ||
		!got.Observation.LastAttemptAt.Equal(attemptedAt) || got.Observation.ObservedAt == nil ||
		!got.Observation.ObservedAt.Equal(observedAt) {
		t.Fatalf("stale repository observation: %#v", got)
	}
}

// TestRepositoryObserverRestartsOnTopologyChangeDespiteStoppedGhostEnvironment
// proves that a stopped environment whose worktree has disappeared does not
// suppress the restart that registers a newly discovered worktree, while a
// live environment in the same situation still does.
func TestRepositoryObserverRestartsOnTopologyChangeDespiteStoppedGhostEnvironment(t *testing.T) {
	observedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name          string
		ghostState    string
		expectRestart bool
	}{
		{name: "stopped ghost", ghostState: "stopped", expectRestart: true},
		{name: "running ghost", ghostState: "running", expectRestart: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			previous := inventoryTestRepository("repo_test", "worktree_gone", "/tmp/sample-gone")
			latest := inventoryTestRepository("repo_test", "worktree_new", "/tmp/sample-new")
			latest.Observation = &contractv2.RepositoryObservation{ObservedAt: &observedAt, LastAttemptAt: observedAt}
			ghost := contractv2.Environment{
				ID: "environment_ghost", RepositoryID: "repo_test", WorktreeID: "worktree_gone",
				ObservedState: testCase.ghostState, DesiredState: testCase.ghostState,
			}
			store := &fakeRepositoryObserverStore{snapshot: contractv2.StatusSnapshot{
				Repositories: []contractv2.Repository{previous}, Environments: []contractv2.Environment{ghost},
				Alerts: []contractv2.Alert{},
			}}
			restarts := 0
			observer := &repositoryObserver{
				store: store, interval: time.Second, now: func() time.Time { return observedAt },
				discover: func(context.Context, time.Time) repositoryInventory {
					return repositoryInventory{
						Repositories: []contractv2.Repository{latest}, Alerts: []contractv2.Alert{},
						Complete: true, AttemptedAt: observedAt,
					}
				},
				annotate: func(applicationPaths, *repositoryInventory) error { return nil },
				restore:  func(context.Context, *repositoryInventory) error { return nil },
				restart:  func() { restarts++ },
			}
			if err := observer.RefreshOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if (restarts == 1) != testCase.expectRestart {
				t.Fatalf("restarts=%d expectRestart=%v", restarts, testCase.expectRestart)
			}
			// The ghost worktree stays published so the environment remains addressable.
			if len(store.snapshot.Repositories) != 1 || len(store.snapshot.Repositories[0].Worktrees) != 2 {
				t.Fatalf("ghost worktree was dropped: %#v", store.snapshot.Repositories)
			}
		})
	}
}
