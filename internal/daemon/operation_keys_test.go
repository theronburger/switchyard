package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	actioncontrol "github.com/theronburger/switchyard/internal/control/action"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/domain"
)

const serializationProbe = 50 * time.Millisecond

func TestOperationKeysSerializeOnlySharedKeys(t *testing.T) {
	keys := NewOperationKeys()
	releaseFirst, err := keys.Acquire(context.Background(), "worktree:a", "repository:r")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		release, err := keys.Acquire(context.Background(), "repository:r", "worktree:b")
		if err != nil {
			t.Error(err)
			return
		}
		release()
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("a shared repository key did not serialize")
	case <-time.After(serializationProbe):
	}
	unrelated, err := keys.Acquire(context.Background(), "worktree:c")
	if err != nil {
		t.Fatal(err)
	}
	unrelated()
	releaseFirst()
	releaseFirst() // release is idempotent
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never acquired after release")
	}
	keys.mutex.Lock()
	defer keys.mutex.Unlock()
	if len(keys.locks) != 0 {
		t.Fatalf("keys leaked: %d", len(keys.locks))
	}
}

func TestOperationKeysAbandonedWaiterNeverStealsTheHolderSlot(t *testing.T) {
	keys := NewOperationKeys()
	release, err := keys.Acquire(context.Background(), "worktree:a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := keys.Acquire(ctx, "worktree:a")
		waiterDone <- err
	}()
	time.Sleep(serializationProbe)
	cancel()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("abandoned waiter error: %v", err)
	}
	// The holder still owns the key: a fresh waiter must still block.
	late := make(chan struct{})
	go func() {
		release, err := keys.Acquire(context.Background(), "worktree:a")
		if err == nil {
			release()
		}
		close(late)
	}()
	select {
	case <-late:
		t.Fatal("the abandoned waiter drained the holder's slot")
	case <-time.After(serializationProbe):
	}
	release()
	<-late
}

func TestOperationKeysTakeFreeKeysEvenAfterContextEnds(t *testing.T) {
	keys := NewOperationKeys()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	release, err := keys.Acquire(ctx, "worktree:a")
	if err != nil {
		t.Fatalf("free key refused: %v", err)
	}
	defer release()
	if _, err := keys.Acquire(ctx, "worktree:a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("contended key with ended context: %v", err)
	}
}

type blockingGate struct {
	entered chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func newBlockingGate() *blockingGate {
	return &blockingGate{entered: make(chan struct{}), proceed: make(chan struct{})}
}

func (gate *blockingGate) wait(ctx context.Context) error {
	gate.once.Do(func() { close(gate.entered) })
	select {
	case <-gate.proceed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (gate *blockingGate) release() { close(gate.proceed) }

func awaitEntered(t *testing.T, gate *blockingGate, what string) {
	t.Helper()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s never started", what)
	}
}

func assertStillPending(t *testing.T, store *fakeActionOperationStore, operationID, what string) {
	t.Helper()
	time.Sleep(serializationProbe)
	operation, err := store.ReadOperation(context.Background(), operationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != string(domain.OperationPending) {
		t.Fatalf("%s ran concurrently with its conflict: %+v", what, operation)
	}
}

func awaitState(t *testing.T, store *fakeActionOperationStore, operationID, state string) contractv2.Operation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		operation, err := store.ReadOperation(context.Background(), operationID)
		if err != nil {
			t.Fatal(err)
		}
		if operation.State == state {
			return operation
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s never reached %s: %+v", operationID, state, operation)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func prepareRequest(worktreeID, key string) contractv2.PrepareWorktreeRequest {
	return contractv2.PrepareWorktreeRequest{
		MutationRequest: contractv2.MutationRequest{
			SchemaVersion: contractv2.SchemaVersion, RequestID: "request_" + key, IdempotencyKey: key,
		},
		WorktreeID: worktreeID,
	}
}

func archiveRequest(worktreeID, key string) contractv2.ArchiveWorktreeRequest {
	return contractv2.ArchiveWorktreeRequest{
		MutationRequest: contractv2.MutationRequest{
			SchemaVersion: contractv2.SchemaVersion, RequestID: "request_" + key, IdempotencyKey: key,
		},
		WorktreeID: worktreeID,
	}
}

func sequentialIDs(prefix string) func(string) (string, error) {
	var counter atomic.Int32
	return func(string) (string, error) {
		return prefix + "_" + string(rune('a'+counter.Add(1))), nil
	}
}

func TestWorkspaceActionServiceSerializesArchiveBehindPreparationOfTheSameWorktree(t *testing.T) {
	store := newFakeActionOperationStore()
	keys := NewOperationKeys()
	gate := newBlockingGate()
	var archives atomic.Int32
	var otherArchives atomic.Int32
	ensurer := fakeWorkspaceEnsurer{ensure: func(ctx context.Context, request workspacecontrol.EnsureRequest) (workspacecontrol.Result, error) {
		if err := gate.wait(ctx); err != nil {
			return workspacecontrol.Result{}, err
		}
		return workspacecontrol.Result{WorktreeID: request.WorktreeID, State: workspacecontrol.StateReady}, nil
	}}
	backend := fakeManagedWorkspaceBackend{
		create: func(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
		archive: func(_ context.Context, request workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error) {
			if request.WorktreePath == "/tmp/worktree_01" {
				archives.Add(1)
			} else {
				otherArchives.Add(1)
			}
			return workspacecontrol.ManagedResult{}, nil
		},
	}
	service, err := NewWorkspaceActionService(WorkspaceActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Backend: backend, Ensurer: ensurer,
		Resolver: fakeWorkspaceActionResolver{}, NewID: sequentialIDs("operation"), Keys: keys,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepare, err := service.PrepareWorktree(context.Background(), prepareRequest("worktree_01", "prepare:01"))
	if err != nil {
		t.Fatal(err)
	}
	awaitEntered(t, gate, "preparation")
	archive, err := service.ArchiveWorktree(context.Background(), archiveRequest("worktree_01", "archive:01"))
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := service.ArchiveWorktree(context.Background(), archiveRequest("worktree_02", "archive:02"))
	if err != nil {
		t.Fatal(err)
	}
	// The unrelated worktree shares the repository key with the archive
	// queued behind the preparation, but not with the preparation itself.
	awaitState(t, store, unrelated.OperationID, string(domain.OperationSucceeded))
	if otherArchives.Load() != 1 {
		t.Fatalf("unrelated archive did not run concurrently: %d", otherArchives.Load())
	}
	assertStillPending(t, store, archive.OperationID, "archive")
	if archives.Load() != 0 {
		t.Fatal("archive removed the worktree while its preparation was running")
	}
	gate.release()
	awaitState(t, store, prepare.OperationID, string(domain.OperationSucceeded))
	awaitState(t, store, archive.OperationID, string(domain.OperationSucceeded))
	if archives.Load() != 1 {
		t.Fatalf("archive calls: %d", archives.Load())
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceActionServiceFailsQueuedOperationAsInterruptedOnClose(t *testing.T) {
	store := newFakeActionOperationStore()
	keys := NewOperationKeys()
	gate := newBlockingGate()
	var archives atomic.Int32
	ensurer := fakeWorkspaceEnsurer{ensure: func(ctx context.Context, request workspacecontrol.EnsureRequest) (workspacecontrol.Result, error) {
		if err := gate.wait(ctx); err != nil {
			return workspacecontrol.Result{}, err
		}
		return workspacecontrol.Result{WorktreeID: request.WorktreeID}, nil
	}}
	backend := fakeManagedWorkspaceBackend{
		create: func(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
		archive: func(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error) {
			archives.Add(1)
			return workspacecontrol.ManagedResult{}, nil
		},
	}
	service, err := NewWorkspaceActionService(WorkspaceActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Backend: backend, Ensurer: ensurer,
		Resolver: fakeWorkspaceActionResolver{}, NewID: sequentialIDs("operation"), Keys: keys,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepare, err := service.PrepareWorktree(context.Background(), prepareRequest("worktree_01", "prepare:01"))
	if err != nil {
		t.Fatal(err)
	}
	awaitEntered(t, gate, "preparation")
	archive, err := service.ArchiveWorktree(context.Background(), archiveRequest("worktree_01", "archive:01"))
	if err != nil {
		t.Fatal(err)
	}
	assertStillPending(t, store, archive.OperationID, "archive")
	waitContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	interrupted := awaitState(t, store, prepare.OperationID, string(domain.OperationFailed))
	if interrupted.Error == nil || interrupted.Error.Code != "WORKSPACE_ACTION_INTERRUPTED" {
		t.Fatalf("interrupted preparation: %+v", interrupted)
	}
	queued := awaitState(t, store, archive.OperationID, string(domain.OperationFailed))
	if queued.Error == nil || queued.Error.Code != "WORKSPACE_ACTION_INTERRUPTED" || archives.Load() != 0 {
		t.Fatalf("queued archive: %+v archives=%d", queued, archives.Load())
	}
}

func TestProfileActionServiceSerializesWorktreeCommandActionsAgainstPreparation(t *testing.T) {
	store := newFakeActionOperationStore()
	keys := NewOperationKeys()
	gate := newBlockingGate()
	ensurer := fakeWorkspaceEnsurer{ensure: func(ctx context.Context, request workspacecontrol.EnsureRequest) (workspacecontrol.Result, error) {
		if err := gate.wait(ctx); err != nil {
			return workspacecontrol.Result{}, err
		}
		return workspacecontrol.Result{WorktreeID: request.WorktreeID}, nil
	}}
	backend := fakeManagedWorkspaceBackend{
		create: func(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
		archive: func(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
	}
	workspace, err := NewWorkspaceActionService(WorkspaceActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Backend: backend, Ensurer: ensurer,
		Resolver: fakeWorkspaceActionResolver{}, NewID: sequentialIDs("workspace"), Keys: keys,
	})
	if err != nil {
		t.Fatal(err)
	}
	var active, peak, runs atomic.Int32
	runner := &fakeProfileActionRunner{run: func(actioncontrol.ExactCommand) (actioncontrol.Outcome, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := peak.Load()
			if current <= observed || peak.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(serializationProbe)
		runs.Add(1)
		return actioncontrol.Outcome{}, nil
	}}
	actions, err := NewProfileActionService(ProfileActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Resolver: newFakeProfileActionResolver(), Runner: runner,
		NewID: sequentialIDs("action"), Keys: keys,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepare, err := workspace.PrepareWorktree(context.Background(), prepareRequest("worktree_01", "prepare:01"))
	if err != nil {
		t.Fatal(err)
	}
	awaitEntered(t, gate, "preparation")

	tidy := func(worktreeID, key string) contractv2.MutationReceipt {
		t.Helper()
		request := actionRequest("tidy")
		request.WorktreeID = worktreeID
		request.IdempotencyKey = key
		receipt, err := actions.RunAction(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		return receipt
	}
	// Two "install"-like runs on the prepared worktree and one elsewhere.
	first := tidy("worktree_01", "tidy:01")
	second := tidy("worktree_01", "tidy:02")
	elsewhere := tidy("worktree_02", "tidy:03")
	awaitState(t, store, elsewhere.OperationID, string(domain.OperationSucceeded))
	assertStillPending(t, store, first.OperationID, "first worktree action")
	assertStillPending(t, store, second.OperationID, "second worktree action")
	gate.release()
	awaitState(t, store, prepare.OperationID, string(domain.OperationSucceeded))
	awaitState(t, store, first.OperationID, string(domain.OperationSucceeded))
	awaitState(t, store, second.OperationID, string(domain.OperationSucceeded))
	if runs.Load() != 3 || peak.Load() != 1 {
		t.Fatalf("worktree actions overlapped: runs=%d peak=%d", runs.Load(), peak.Load())
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := actions.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	if err := workspace.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
}

func TestProfileActionServiceRefusesResolutionWithoutExecutingWorktree(t *testing.T) {
	store := newFakeActionOperationStore()
	resolver := newFakeProfileActionResolver()
	resolver.definitions["tidy"] = actioncontrol.Definition{ID: "tidy", DisplayName: "Tidy", Scope: "worktree", Risk: "local", Kind: "command"}
	runner := &fakeProfileActionRunner{}
	service, err := NewProfileActionService(ProfileActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Resolver: inconsistentWorktreeResolver{resolver}, Runner: runner,
		NewID: sequentialIDs("action"),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := actionRequest("tidy")
	request.WorktreeID = "worktree_01"
	_, err = service.RunAction(context.Background(), request)
	var actionError *ActionError
	if !errors.As(err, &actionError) || actionError.Contract.Code != "ACTIONS_UNAVAILABLE" || runner.calls.Load() != 0 {
		t.Fatalf("inconsistent worktree resolution: err=%v runs=%d", err, runner.calls.Load())
	}
}

type inconsistentWorktreeResolver struct{ *fakeProfileActionResolver }

func (resolver inconsistentWorktreeResolver) ResolveAction(ctx context.Context, request contractv2.RunProfileActionRequest) (ProfileActionResolution, error) {
	resolution, err := resolver.fakeProfileActionResolver.ResolveAction(ctx, request)
	resolution.WorktreeID = ""
	return resolution, err
}

func TestEnvironmentActionServiceHoldsWorktreeKeyUntilStartPublishes(t *testing.T) {
	store := newFakeActionOperationStore()
	keys := NewOperationKeys()
	gate := newBlockingGate()
	var archives atomic.Int32
	coordinator := fakeActionCoordinator{
		start: func(ctx context.Context, request environmentcontrol.StartRequest) (environmentcontrol.EnvironmentResult, error) {
			return environmentcontrol.EnvironmentResult{}, gate.wait(ctx)
		},
		stop: func(context.Context, environmentcontrol.StopRequest) (environmentcontrol.EnvironmentResult, error) {
			return environmentcontrol.EnvironmentResult{}, nil
		},
	}
	environment, err := NewEnvironmentActionService(EnvironmentActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Journal: fakeActionJournal{}, Coordinator: coordinator,
		Workspace: noOpWorkspaceEnsurer(),
		Resolver: fakeActionResolver{start: EnvironmentStartResolution{
			EnvironmentID: "environment_01", WorktreeID: "worktree_01",
			Intent: environmentcontrol.PlanIntent{ProfileDigest: "sample", ServiceIDs: []string{"storefront"}},
		}},
		NewID: sequentialIDs("environment"), Keys: keys,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakeManagedWorkspaceBackend{
		create: func(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
		archive: func(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error) {
			archives.Add(1)
			return workspacecontrol.ManagedResult{}, nil
		},
	}
	workspace, err := NewWorkspaceActionService(WorkspaceActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Backend: backend, Ensurer: noOpWorkspaceEnsurer(),
		Resolver: fakeWorkspaceActionResolver{}, NewID: sequentialIDs("workspace"), Keys: keys,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := environment.StartEnvironment(context.Background(), validActionStartRequest()); err != nil {
		t.Fatal(err)
	}
	awaitEntered(t, gate, "environment start")
	archive, err := workspace.ArchiveWorktree(context.Background(), archiveRequest("worktree_01", "archive:01"))
	if err != nil {
		t.Fatal(err)
	}
	assertStillPending(t, store, archive.OperationID, "archive")
	if archives.Load() != 0 {
		t.Fatal("archive ran while the environment was still starting")
	}
	gate.release()
	awaitState(t, store, archive.OperationID, string(domain.OperationSucceeded))
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := environment.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	if err := workspace.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
}
