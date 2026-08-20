package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/domain"
)

type fakeManagedWorkspaceBackend struct {
	create  func(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error)
	adopt   func(context.Context, workspacecontrol.AdoptManagedRequest) (workspacecontrol.ManagedResult, error)
	archive func(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error)
}

func (backend fakeManagedWorkspaceBackend) Create(
	ctx context.Context,
	request workspacecontrol.CreateManagedRequest,
) (workspacecontrol.ManagedResult, error) {
	return backend.create(ctx, request)
}

func (backend fakeManagedWorkspaceBackend) Archive(
	ctx context.Context,
	request workspacecontrol.ArchiveManagedRequest,
) (workspacecontrol.ManagedResult, error) {
	return backend.archive(ctx, request)
}

func (backend fakeManagedWorkspaceBackend) Adopt(
	ctx context.Context,
	request workspacecontrol.AdoptManagedRequest,
) (workspacecontrol.ManagedResult, error) {
	if backend.adopt == nil {
		return workspacecontrol.ManagedResult{}, nil
	}
	return backend.adopt(ctx, request)
}

type fakeWorkspaceActionResolver struct{}

func (fakeWorkspaceActionResolver) ResolveCreate(
	_ context.Context,
	request contractv2.CreateWorktreeRequest,
) (workspacecontrol.CreateManagedRequest, error) {
	return workspacecontrol.CreateManagedRequest{
		RepositoryID: request.RepositoryID, Branch: request.Branch, StartPoint: request.StartPoint,
	}, nil
}

func (fakeWorkspaceActionResolver) ResolveArchive(
	_ context.Context,
	request contractv2.ArchiveWorktreeRequest,
) (workspacecontrol.ArchiveManagedRequest, error) {
	return workspacecontrol.ArchiveManagedRequest{
		RepositoryID: "repository_01", WorktreePath: "/tmp/" + request.WorktreeID,
	}, nil
}

func (fakeWorkspaceActionResolver) ResolveAdopt(
	_ context.Context,
	request contractv2.AdoptWorktreeRequest,
) (workspacecontrol.AdoptManagedRequest, error) {
	return workspacecontrol.AdoptManagedRequest{
		RepositoryID: "repository_01", WorktreePath: "/tmp/" + request.WorktreeID,
	}, nil
}

func (fakeWorkspaceActionResolver) ResolvePrepare(
	_ context.Context,
	request contractv2.PrepareWorktreeRequest,
) (string, error) {
	return request.WorktreeID, nil
}

func noOpWorkspaceEnsurer() fakeWorkspaceEnsurer {
	return fakeWorkspaceEnsurer{ensure: func(
		_ context.Context,
		request workspacecontrol.EnsureRequest,
	) (workspacecontrol.Result, error) {
		return workspacecontrol.Result{WorktreeID: request.WorktreeID}, nil
	}}
}

func TestWorkspaceActionServiceIsIdempotentAndRestartsOnlyAfterSuccess(t *testing.T) {
	store := newFakeActionOperationStore()
	var createCalls atomic.Int32
	var restarts atomic.Int32
	backend := fakeManagedWorkspaceBackend{
		create: func(
			_ context.Context,
			request workspacecontrol.CreateManagedRequest,
		) (workspacecontrol.ManagedResult, error) {
			createCalls.Add(1)
			if request.RepositoryID != "repository_01" || request.Branch != "feature/example" {
				t.Errorf("create request: %+v", request)
			}
			return workspacecontrol.ManagedResult{}, nil
		},
		archive: func(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
	}
	service, err := NewWorkspaceActionService(WorkspaceActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Backend: backend, Ensurer: noOpWorkspaceEnsurer(),
		Resolver: fakeWorkspaceActionResolver{}, Restart: func() { restarts.Add(1) },
		NewID: func(string) (string, error) { return "operation_01", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := validCreateWorktreeRequest()
	first, err := service.CreateWorktree(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateWorktree(context.Background(), request)
	if err != nil || second.OperationID != first.OperationID {
		t.Fatalf("idempotent receipt: first=%+v second=%+v err=%v", first, second, err)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	operation, err := store.ReadOperation(context.Background(), first.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if createCalls.Load() != 1 || restarts.Load() != 1 || operation.State != string(domain.OperationSucceeded) {
		t.Fatalf("workspace action: calls=%d restarts=%d operation=%+v", createCalls.Load(), restarts.Load(), operation)
	}
}

func TestWorkspaceActionServiceReportsSafeArchiveFailureWithoutRestart(t *testing.T) {
	store := newFakeActionOperationStore()
	var restarts atomic.Int32
	backend := fakeManagedWorkspaceBackend{
		create: func(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
		archive: func(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, errors.Join(
				workspacecontrol.ErrManagedDirty, errors.New("private /Users/person/path"),
			)
		},
	}
	service, err := NewWorkspaceActionService(WorkspaceActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Backend: backend, Ensurer: noOpWorkspaceEnsurer(),
		Resolver: fakeWorkspaceActionResolver{}, Restart: func() { restarts.Add(1) },
		NewID: func(string) (string, error) { return "operation_01", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.ArchiveWorktree(context.Background(), contractv2.ArchiveWorktreeRequest{
		MutationRequest: contractv2.MutationRequest{
			SchemaVersion: contractv2.SchemaVersion, RequestID: "request_01", IdempotencyKey: "archive_01",
		},
		WorktreeID: "worktree_01",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	operation, err := store.ReadOperation(context.Background(), receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if restarts.Load() != 0 || operation.State != string(domain.OperationFailed) ||
		operation.Error == nil || operation.Error.Code != "WORKSPACE_DIRTY" ||
		operation.Error.Message != "The worktree has local changes and cannot be modified safely." ||
		operation.Error.Retryable || operation.Error.ResourceKind != "worktree" ||
		operation.Error.ResourceID != "worktree_01" || operation.Error.NextAction != "commit_or_stash_changes" {
		t.Fatalf("failed workspace action: restarts=%d operation=%+v", restarts.Load(), operation)
	}
}

func TestWorkspaceActionServiceAdoptsAndRestartsAfterSuccess(t *testing.T) {
	store := newFakeActionOperationStore()
	var adoptCalls atomic.Int32
	var restarts atomic.Int32
	backend := fakeManagedWorkspaceBackend{
		create: func(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
		adopt: func(_ context.Context, request workspacecontrol.AdoptManagedRequest) (workspacecontrol.ManagedResult, error) {
			adoptCalls.Add(1)
			if request.RepositoryID != "repository_01" || request.WorktreePath != "/tmp/worktree_01" {
				t.Fatalf("adopt request: %+v", request)
			}
			return workspacecontrol.ManagedResult{}, nil
		},
		archive: func(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
	}
	service, err := NewWorkspaceActionService(WorkspaceActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Backend: backend, Ensurer: noOpWorkspaceEnsurer(),
		Resolver: fakeWorkspaceActionResolver{}, Restart: func() { restarts.Add(1) },
		NewID: func(string) (string, error) { return "operation_adopt", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.AdoptWorktree(context.Background(), contractv2.AdoptWorktreeRequest{
		MutationRequest: contractv2.MutationRequest{
			SchemaVersion: contractv2.SchemaVersion, RequestID: "request_adopt", IdempotencyKey: "adopt:key",
		},
		WorktreeID: "worktree_01",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	operation, err := store.ReadOperation(context.Background(), receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if adoptCalls.Load() != 1 || restarts.Load() != 1 || operation.Kind != workspaceAdoptOperation ||
		operation.State != string(domain.OperationSucceeded) {
		t.Fatalf("adoption: calls=%d restarts=%d operation=%+v", adoptCalls.Load(), restarts.Load(), operation)
	}
}

func TestWorkspaceActionServicePreparesWithoutRestartingInventory(t *testing.T) {
	store := newFakeActionOperationStore()
	var ensureCalls atomic.Int32
	var restarts atomic.Int32
	ensurer := fakeWorkspaceEnsurer{ensure: func(
		_ context.Context,
		request workspacecontrol.EnsureRequest,
	) (workspacecontrol.Result, error) {
		ensureCalls.Add(1)
		if request.OperationID != "operation_prepare" || request.WorktreeID != "worktree_01" {
			t.Fatalf("ensure request: %+v", request)
		}
		return workspacecontrol.Result{WorktreeID: request.WorktreeID, State: workspacecontrol.StateReady}, nil
	}}
	backend := fakeManagedWorkspaceBackend{
		create: func(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
		archive: func(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
	}
	service, err := NewWorkspaceActionService(WorkspaceActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Backend: backend, Ensurer: ensurer,
		Resolver: fakeWorkspaceActionResolver{}, Restart: func() { restarts.Add(1) },
		NewID: func(string) (string, error) { return "operation_prepare", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.PrepareWorktree(context.Background(), contractv2.PrepareWorktreeRequest{
		MutationRequest: contractv2.MutationRequest{
			SchemaVersion: contractv2.SchemaVersion, RequestID: "request_prepare", IdempotencyKey: "prepare:key",
		},
		WorktreeID: "worktree_01",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	operation, err := store.ReadOperation(context.Background(), receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if ensureCalls.Load() != 1 || restarts.Load() != 0 || operation.Kind != workspacePrepareOperation ||
		operation.State != string(domain.OperationSucceeded) {
		t.Fatalf("preparation: ensures=%d restarts=%d operation=%+v", ensureCalls.Load(), restarts.Load(), operation)
	}
}

func TestWorkspaceActionServiceRecordsPreparationFailure(t *testing.T) {
	store := newFakeActionOperationStore()
	ensurer := fakeWorkspaceEnsurer{ensure: func(
		context.Context,
		workspacecontrol.EnsureRequest,
	) (workspacecontrol.Result, error) {
		return workspacecontrol.Result{}, workspacecontrol.ErrStepFailed
	}}
	backend := fakeManagedWorkspaceBackend{
		create: func(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
		archive: func(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error) {
			return workspacecontrol.ManagedResult{}, nil
		},
	}
	service, err := NewWorkspaceActionService(WorkspaceActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Backend: backend, Ensurer: ensurer,
		Resolver: fakeWorkspaceActionResolver{}, NewID: func(string) (string, error) { return "operation_prepare", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.PrepareWorktree(context.Background(), contractv2.PrepareWorktreeRequest{
		MutationRequest: contractv2.MutationRequest{
			SchemaVersion: contractv2.SchemaVersion, RequestID: "request_prepare", IdempotencyKey: "prepare:key",
		},
		WorktreeID: "worktree_01",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	operation, err := store.ReadOperation(context.Background(), receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != string(domain.OperationFailed) || operation.Error == nil ||
		operation.Error.Code != "WORKSPACE_ACTION_FAILED" || operation.Error.ResourceID != "worktree_01" {
		t.Fatalf("failed preparation: %+v", operation)
	}
}

func TestWorkspaceActionServicePreservesCancellationWhenInitialTransitionIsInterrupted(t *testing.T) {
	store := newFakeActionOperationStore()
	transitionStarted := make(chan struct{})
	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	store.transition = func(
		ctx context.Context,
		operationID string,
		nextState string,
		failure *contractv2.ContractError,
	) (contractv2.Operation, error) {
		if nextState == string(domain.OperationRunning) {
			close(transitionStarted)
			<-ctx.Done()
			return contractv2.Operation{}, ctx.Err()
		}
		store.mutex.Lock()
		defer store.mutex.Unlock()
		operation := store.operations[operationID]
		operation.State = nextState
		operation.Error = failure
		store.operations[operationID] = operation
		return operation, nil
	}
	service, err := NewWorkspaceActionService(WorkspaceActionServiceConfig{
		Lifecycle: lifecycle, Store: store,
		Backend: fakeManagedWorkspaceBackend{
			create: func(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error) {
				return workspacecontrol.ManagedResult{}, nil
			},
			archive: func(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error) {
				return workspacecontrol.ManagedResult{}, nil
			},
		},
		Ensurer: noOpWorkspaceEnsurer(), Resolver: fakeWorkspaceActionResolver{},
		NewID: func(string) (string, error) { return "operation_prepare", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := service.PrepareWorktree(context.Background(), contractv2.PrepareWorktreeRequest{
		MutationRequest: contractv2.MutationRequest{
			SchemaVersion: contractv2.SchemaVersion, RequestID: "request_prepare", IdempotencyKey: "prepare:key",
		},
		WorktreeID: "worktree_01",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-transitionStarted
	cancelLifecycle()
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	operation, err := store.ReadOperation(context.Background(), receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != string(domain.OperationFailed) || operation.Error == nil ||
		operation.Error.Code != "WORKSPACE_ACTION_INTERRUPTED" || operation.Error.NextAction != "retry" {
		t.Fatalf("interrupted operation: %+v", operation)
	}
}

func validCreateWorktreeRequest() contractv2.CreateWorktreeRequest {
	return contractv2.CreateWorktreeRequest{
		MutationRequest: contractv2.MutationRequest{
			SchemaVersion: contractv2.SchemaVersion, RequestID: "request_01", IdempotencyKey: "create_01",
		},
		RepositoryID: "repository_01", Branch: "feature/example",
	}
}
