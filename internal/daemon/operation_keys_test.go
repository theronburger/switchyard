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
	if !keys.idle() {
		t.Fatal("keys leaked after every holder released")
	}
}

// queued blocks until exactly count operations are waiting, so a test can
// assert "still pending" against the queue rather than against a timer.
func (keys *OperationKeys) queued(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		keys.mutex.Lock()
		waiting := len(keys.waiters)
		keys.mutex.Unlock()
		if waiting == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("never reached %d queued operations", count)
}

type keyAcquisition struct {
	granted chan func()
	failed  chan error
}

func acquireAsync(keys *OperationKeys, ctx context.Context, names ...string) keyAcquisition {
	acquisition := keyAcquisition{granted: make(chan func(), 1), failed: make(chan error, 1)}
	go func() {
		release, err := keys.Acquire(ctx, names...)
		if err != nil {
			acquisition.failed <- err
			return
		}
		acquisition.granted <- release
	}()
	return acquisition
}

func (acquisition keyAcquisition) mustStayPending(t *testing.T) {
	t.Helper()
	select {
	case <-acquisition.granted:
		t.Fatal("a conflicting operation was granted")
	case err := <-acquisition.failed:
		t.Fatalf("a pending operation failed: %v", err)
	default:
	}
}

func (acquisition keyAcquisition) mustBeGranted(t *testing.T) func() {
	t.Helper()
	select {
	case release := <-acquisition.granted:
		return release
	case err := <-acquisition.failed:
		t.Fatalf("acquisition failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("acquisition never granted")
	}
	return nil
}

func TestOperationKeysMutualExclusionUnderContention(t *testing.T) {
	keys := NewOperationKeys()
	var active, maxActive int32
	var wg sync.WaitGroup
	for index := 0; index < 32; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			names := []string{"worktree:a"}
			if index%3 == 0 {
				names = append(names, "repository:r")
			}
			release, err := keys.Acquire(context.Background(), names...)
			if err != nil {
				t.Error(err)
				return
			}
			current := atomic.AddInt32(&active, 1)
			for {
				seen := atomic.LoadInt32(&maxActive)
				if current <= seen || atomic.CompareAndSwapInt32(&maxActive, seen, current) {
					break
				}
			}
			time.Sleep(200 * time.Microsecond)
			atomic.AddInt32(&active, -1)
			release()
		}(index)
	}
	wg.Wait()
	if maxActive != 1 {
		t.Fatalf("%d operations held worktree:a at once", maxActive)
	}
	if !keys.idle() {
		t.Fatal("keys leaked")
	}
}

func TestOperationKeysUnrelatedOperationsNeverWait(t *testing.T) {
	keys := NewOperationKeys()
	releasePreparation, err := keys.Acquire(context.Background(), "worktree:a")
	if err != nil {
		t.Fatal(err)
	}
	archive := acquireAsync(keys, context.Background(), "worktree:a", "repository:r")
	keys.queued(t, 1)
	archive.mustStayPending(t)
	// The archive holds nothing while it waits: a different worktree of the
	// same repository and a different repository both proceed immediately.
	releaseOther, err := keys.Acquire(context.Background(), "worktree:b")
	if err != nil {
		t.Fatal(err)
	}
	releaseForeign, err := keys.Acquire(context.Background(), "repository:other", "worktree:c")
	if err != nil {
		t.Fatal(err)
	}
	archive.mustStayPending(t)
	releaseOther()
	releaseForeign()
	releasePreparation()
	archive.mustBeGranted(t)()
	if !keys.idle() {
		t.Fatal("keys leaked")
	}
}

func TestOperationKeysCancelledWaiterLeavesTheQueueWithoutHoldingKeys(t *testing.T) {
	keys := NewOperationKeys()
	releaseHolder, err := keys.Acquire(context.Background(), "worktree:a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	archive := acquireAsync(keys, ctx, "worktree:a", "repository:r")
	keys.queued(t, 1)
	// The queued archive reserves nothing: a repository-only operation runs
	// immediately even though it shares the repository key with the archive.
	releaseCreate, err := keys.Acquire(context.Background(), "repository:r")
	if err != nil {
		t.Fatal(err)
	}
	archive.mustStayPending(t)
	cancel()
	select {
	case err := <-archive.failed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter error: %v", err)
		}
	case <-archive.granted:
		t.Fatal("a cancelled waiter was granted")
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation never returned")
	}
	keys.queued(t, 0)
	releaseCreate()
	releaseHolder()
	if !keys.idle() {
		t.Fatal("a cancelled waiter left a key held or queued")
	}
	// Nothing lingers from the cancelled waiter: both keys are free again.
	release, err := keys.Acquire(context.Background(), "worktree:a", "repository:r")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestOperationKeysTwoWorktreeRepositoryWaitersWithRepositoryContentionConverge(t *testing.T) {
	// Regression: the previous retry-loop design livelocked when two
	// worktree+repository waiters competed with repository-only operations,
	// each repeatedly taking one key, finding the other busy, and backing off.
	keys := NewOperationKeys()
	releaseA, err := keys.Acquire(context.Background(), "worktree:a")
	if err != nil {
		t.Fatal(err)
	}
	releaseB, err := keys.Acquire(context.Background(), "worktree:b")
	if err != nil {
		t.Fatal(err)
	}
	releaseRepository, err := keys.Acquire(context.Background(), "repository:r")
	if err != nil {
		t.Fatal(err)
	}
	archiveA := acquireAsync(keys, context.Background(), "worktree:a", "repository:r")
	keys.queued(t, 1)
	archiveB := acquireAsync(keys, context.Background(), "worktree:b", "repository:r")
	keys.queued(t, 2)
	repositoryOnly := make([]keyAcquisition, 0, 8)
	for index := 0; index < 8; index++ {
		repositoryOnly = append(repositoryOnly, acquireAsync(keys, context.Background(), "repository:r"))
		keys.queued(t, 3+index)
	}
	archiveA.mustStayPending(t)
	archiveB.mustStayPending(t)
	for _, acquisition := range repositoryOnly {
		acquisition.mustStayPending(t)
	}

	// Freeing a worktree alone grants nothing: every waiter still needs the
	// held repository key, and no waiter holds a worktree while it waits.
	releaseA()
	releaseB()
	keys.queued(t, 10)
	archiveA.mustStayPending(t)
	archiveB.mustStayPending(t)

	// Freeing the repository grants the oldest waiter whose keys are free:
	// archive A, with both worktrees free, ahead of every later waiter.
	releaseRepository()
	releaseArchiveA := archiveA.mustBeGranted(t)
	keys.queued(t, 9)
	archiveB.mustStayPending(t)
	releaseArchiveA()
	releaseArchiveB := archiveB.mustBeGranted(t)
	keys.queued(t, 8)
	for _, acquisition := range repositoryOnly {
		acquisition.mustStayPending(t)
	}
	releaseArchiveB()
	// The repository-only operations then drain one at a time in queue order.
	for index, acquisition := range repositoryOnly {
		release := acquisition.mustBeGranted(t)
		keys.queued(t, 7-index)
		for _, later := range repositoryOnly[index+1:] {
			later.mustStayPending(t)
		}
		release()
	}
	if !keys.idle() {
		t.Fatal("keys leaked")
	}
}

func TestOperationKeysRandomizedContentionTerminatesAndExcludes(t *testing.T) {
	keys := NewOperationKeys()
	holders := map[string]*int32{}
	for _, name := range []string{"worktree:a", "worktree:b", "worktree:c", "repository:r"} {
		holders[name] = new(int32)
	}
	shapes := [][]string{
		{"worktree:a"}, {"worktree:b"}, {"worktree:c"}, {"repository:r"},
		{"worktree:a", "repository:r"}, {"worktree:b", "repository:r"}, {"worktree:c", "repository:r"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := 0; round < 200; round++ {
				names := shapes[(worker+round)%len(shapes)]
				attempt := ctx
				attemptCancel := func() {}
				if round%5 == 0 {
					attempt, attemptCancel = context.WithTimeout(ctx, time.Duration(round%3)*time.Millisecond)
				}
				release, err := keys.Acquire(attempt, names...)
				attemptCancel()
				if err != nil {
					if ctx.Err() != nil {
						t.Error("contention did not converge before the deadline")
						return
					}
					continue
				}
				for _, name := range names {
					if atomic.AddInt32(holders[name], 1) != 1 {
						t.Errorf("%s held by more than one operation", name)
					}
				}
				for _, name := range names {
					atomic.AddInt32(holders[name], -1)
				}
				release()
			}
		}(worker)
	}
	wg.Wait()
	if !keys.idle() {
		t.Fatal("keys leaked")
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
