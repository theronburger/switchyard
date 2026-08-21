package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/state"
)

const (
	workspaceCreateOperation  = "workspace.create"
	workspaceAdoptOperation   = "workspace.adopt"
	workspaceArchiveOperation = "workspace.archive"
	workspacePrepareOperation = "workspace.prepare"
)

type ManagedWorkspaceBackend interface {
	Create(context.Context, workspacecontrol.CreateManagedRequest) (workspacecontrol.ManagedResult, error)
	Adopt(context.Context, workspacecontrol.AdoptManagedRequest) (workspacecontrol.ManagedResult, error)
	Archive(context.Context, workspacecontrol.ArchiveManagedRequest) (workspacecontrol.ManagedResult, error)
}

type WorkspaceActionResolver interface {
	ResolveCreate(context.Context, contractv2.CreateWorktreeRequest) (workspacecontrol.CreateManagedRequest, error)
	ResolveAdopt(context.Context, contractv2.AdoptWorktreeRequest) (workspacecontrol.AdoptManagedRequest, error)
	ResolveArchive(context.Context, contractv2.ArchiveWorktreeRequest) (workspacecontrol.ArchiveManagedRequest, error)
	ResolvePrepare(context.Context, contractv2.PrepareWorktreeRequest) (string, error)
}

type WorkspaceActionServiceConfig struct {
	Lifecycle context.Context
	Store     EnvironmentActionStore
	Backend   ManagedWorkspaceBackend
	Ensurer   WorkspaceEnsurer
	Resolver  WorkspaceActionResolver
	Restart   func()
	NewID     func(string) (string, error)
	// Keys serializes conflicting operations across services. Share one
	// instance with the environment and profile-action services.
	Keys *OperationKeys
}

type WorkspaceActionService struct {
	store     EnvironmentActionStore
	backend   ManagedWorkspaceBackend
	ensurer   WorkspaceEnsurer
	resolver  WorkspaceActionResolver
	restart   func()
	newID     func(string) (string, error)
	keys      *OperationKeys
	lifecycle context.Context
	cancel    context.CancelFunc
	mutex     sync.Mutex
	closed    bool
	workers   sync.WaitGroup
}

func NewWorkspaceActionService(config WorkspaceActionServiceConfig) (*WorkspaceActionService, error) {
	if config.Lifecycle == nil || config.Store == nil || config.Backend == nil || config.Ensurer == nil || config.Resolver == nil {
		return nil, errors.New("workspace action service dependencies are required")
	}
	if config.Restart == nil {
		config.Restart = func() {}
	}
	if config.NewID == nil {
		config.NewID = randomActionID
	}
	if config.Keys == nil {
		config.Keys = NewOperationKeys()
	}
	lifecycle, cancel := context.WithCancel(config.Lifecycle)
	return &WorkspaceActionService{
		store: config.Store, backend: config.Backend, ensurer: config.Ensurer, resolver: config.Resolver,
		restart: config.Restart, newID: config.NewID, keys: config.Keys, lifecycle: lifecycle, cancel: cancel,
	}, nil
}

func (service *WorkspaceActionService) CreateWorktree(
	ctx context.Context,
	request contractv2.CreateWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	if request.Validate() != nil {
		return contractv2.MutationReceipt{}, invalidWorkspaceAction()
	}
	resolved, err := service.resolver.ResolveCreate(ctx, request)
	if err != nil {
		return contractv2.MutationReceipt{}, safeResolutionError(err)
	}
	fingerprint, err := state.FingerprintRequest(request)
	if err != nil {
		return contractv2.MutationReceipt{}, invalidWorkspaceAction()
	}
	return service.accept(workspaceAcceptance{
		ctx: ctx, request: request.MutationRequest, fingerprint: fingerprint, kind: workspaceCreateOperation,
		resourceKind: "repository", resourceID: request.RepositoryID, restartAfterSuccess: true,
		// Creation mutates the repository's shared Git administrative state.
		keys: []string{repositoryOperationKey(resolved.RepositoryID)},
	}, func(string) error {
		_, err := service.backend.Create(service.lifecycle, resolved)
		return err
	})
}

func (service *WorkspaceActionService) ArchiveWorktree(
	ctx context.Context,
	request contractv2.ArchiveWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	if request.Validate() != nil {
		return contractv2.MutationReceipt{}, invalidWorkspaceAction()
	}
	resolved, err := service.resolver.ResolveArchive(ctx, request)
	if err != nil {
		return contractv2.MutationReceipt{}, safeResolutionError(err)
	}
	fingerprint, err := state.FingerprintRequest(request)
	if err != nil {
		return contractv2.MutationReceipt{}, invalidWorkspaceAction()
	}
	return service.accept(workspaceAcceptance{
		ctx: ctx, request: request.MutationRequest, fingerprint: fingerprint, kind: workspaceArchiveOperation,
		resourceKind: "worktree", resourceID: request.WorktreeID, restartAfterSuccess: true,
		// Archive waits for any preparation or worktree-scoped action on the
		// same worktree and for sibling Git administrative mutations.
		keys: []string{worktreeOperationKey(request.WorktreeID), repositoryOperationKey(resolved.RepositoryID)},
	}, func(string) error {
		// Revalidate immediately before mutation: an environment start may
		// have been accepted for this worktree after the request was resolved,
		// and the backend re-checks only Git state, not environment activity.
		revalidated, err := service.resolver.ResolveArchive(service.lifecycle, request)
		if err != nil {
			return safeResolutionError(err)
		}
		if revalidated != resolved {
			return invalidWorkspaceAction()
		}
		_, err = service.backend.Archive(service.lifecycle, revalidated)
		return err
	})
}

func (service *WorkspaceActionService) AdoptWorktree(
	ctx context.Context,
	request contractv2.AdoptWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	if request.Validate() != nil {
		return contractv2.MutationReceipt{}, invalidWorkspaceAction()
	}
	resolved, err := service.resolver.ResolveAdopt(ctx, request)
	if err != nil {
		return contractv2.MutationReceipt{}, safeResolutionError(err)
	}
	fingerprint, err := state.FingerprintRequest(request)
	if err != nil {
		return contractv2.MutationReceipt{}, invalidWorkspaceAction()
	}
	return service.accept(workspaceAcceptance{
		ctx: ctx, request: request.MutationRequest, fingerprint: fingerprint, kind: workspaceAdoptOperation,
		resourceKind: "worktree", resourceID: request.WorktreeID, restartAfterSuccess: true,
		keys: []string{worktreeOperationKey(request.WorktreeID), repositoryOperationKey(resolved.RepositoryID)},
	}, func(string) error {
		_, err := service.backend.Adopt(service.lifecycle, resolved)
		return err
	})
}

func (service *WorkspaceActionService) PrepareWorktree(
	ctx context.Context,
	request contractv2.PrepareWorktreeRequest,
) (contractv2.MutationReceipt, error) {
	if request.Validate() != nil {
		return contractv2.MutationReceipt{}, invalidWorkspaceAction()
	}
	worktreeID, err := service.resolver.ResolvePrepare(ctx, request)
	if err != nil {
		return contractv2.MutationReceipt{}, safeResolutionError(err)
	}
	fingerprint, err := state.FingerprintRequest(request)
	if err != nil {
		return contractv2.MutationReceipt{}, invalidWorkspaceAction()
	}
	return service.accept(
		workspaceAcceptance{
			ctx: ctx, request: request.MutationRequest, fingerprint: fingerprint, kind: workspacePrepareOperation,
			resourceKind: "worktree", resourceID: worktreeID, restartAfterSuccess: false,
			keys: []string{worktreeOperationKey(worktreeID)},
		},
		func(operationID string) error {
			_, ensureErr := service.ensurer.Ensure(service.lifecycle, workspacecontrol.EnsureRequest{
				OperationID: operationID, WorktreeID: worktreeID,
			})
			return ensureErr
		},
	)
}

// workspaceAcceptance describes one accepted workspace mutation: its durable
// operation identity and the resource keys its execution must hold.
type workspaceAcceptance struct {
	ctx                 context.Context
	request             contractv2.MutationRequest
	fingerprint         [sha256.Size]byte
	kind                string
	resourceKind        string
	resourceID          string
	restartAfterSuccess bool
	keys                []string
}

func (service *WorkspaceActionService) accept(
	acceptance workspaceAcceptance,
	action func(string) error,
) (contractv2.MutationReceipt, error) {
	ctx, request, fingerprint, kind := acceptance.ctx, acceptance.request, acceptance.fingerprint, acceptance.kind
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed || service.lifecycle.Err() != nil {
		return contractv2.MutationReceipt{}, actionsUnavailable()
	}
	operationID, err := service.newID("operation")
	if err != nil {
		return contractv2.MutationReceipt{}, actionsUnavailable()
	}
	operation, created, err := service.store.CreateOperation(ctx, state.NewOperation{
		ID: operationID, RequestID: request.RequestID, IdempotencyKey: request.IdempotencyKey,
		RequestFingerprint: fingerprint, Kind: kind,
	})
	if err != nil {
		return contractv2.MutationReceipt{}, operationCreationError(err)
	}
	if created {
		service.workers.Add(1)
		go service.execute(operation.ID, acceptance, action)
	}
	return receiptForOperation(request.RequestID, operation), nil
}

func (service *WorkspaceActionService) execute(
	operationID string,
	acceptance workspaceAcceptance,
	action func(string) error,
) {
	defer service.workers.Done()
	resourceKind, resourceID, restartAfterSuccess := acceptance.resourceKind, acceptance.resourceID, acceptance.restartAfterSuccess
	// The operation stays pending while a conflicting operation on the same
	// worktree or repository runs; a lifecycle shutdown while waiting fails
	// it as interrupted without ever having started.
	release, err := service.keys.Acquire(service.lifecycle, acceptance.keys...)
	if err != nil {
		service.fail(operationID, workspaceFailure(err, resourceKind, resourceID))
		return
	}
	defer release()
	if _, err := service.store.TransitionOperation(
		service.lifecycle, operationID, string(domain.OperationRunning), nil,
	); err != nil {
		service.fail(operationID, workspaceFailure(err, resourceKind, resourceID))
		return
	}
	if err := action(operationID); err != nil {
		service.fail(operationID, workspaceFailure(err, resourceKind, resourceID))
		return
	}
	if _, err := service.store.TransitionOperation(
		service.lifecycle, operationID, string(domain.OperationSucceeded), nil,
	); err != nil {
		service.fail(operationID, workspaceFailure(err, resourceKind, resourceID))
		return
	}
	if restartAfterSuccess {
		service.restart()
	}
}

func (service *WorkspaceActionService) fail(operationID string, failure contractv2.ContractError) {
	ctx, cancel := context.WithTimeout(context.Background(), actionFinalizationTimeout)
	defer cancel()
	_, _ = service.store.TransitionOperation(ctx, operationID, string(domain.OperationFailed), &failure)
}

func (service *WorkspaceActionService) CloseAndWait(ctx context.Context) error {
	service.mutex.Lock()
	if !service.closed {
		service.closed = true
		service.cancel()
	}
	service.mutex.Unlock()
	completed := make(chan struct{})
	go func() {
		service.workers.Wait()
		close(completed)
	}()
	select {
	case <-completed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func workspaceFailure(err error, resourceKind, resourceID string) contractv2.ContractError {
	failure := contractv2.ContractError{
		Code: "WORKSPACE_ACTION_FAILED", Message: "The workspace action could not be completed safely.",
		Retryable: true, ResourceKind: resourceKind, ResourceID: resourceID,
		NextAction: "inspect_workspace_diagnostics",
	}
	var actionError *ActionError
	switch {
	case errors.As(err, &actionError) && validActionError(actionError):
		// A revalidation refused the mutation; publish its exact reason.
		failure.Code = actionError.Contract.Code
		failure.Message = actionError.Contract.Message
		failure.Retryable = actionError.Contract.Retryable
		failure.NextAction = "retry"
	case errors.Is(err, workspacecontrol.ErrManagedDirty):
		failure.Code = "WORKSPACE_DIRTY"
		failure.Message = "The worktree has local changes and cannot be modified safely."
		failure.Retryable = false
		failure.NextAction = "commit_or_stash_changes"
	case errors.Is(err, workspacecontrol.ErrManagedUnpushed):
		failure.Code = "WORKSPACE_UNPUSHED"
		failure.Message = "The worktree has unpushed commits and cannot be archived safely."
		failure.Retryable = false
		failure.NextAction = "push_branch"
	case errors.Is(err, workspacecontrol.ErrManagedForeign):
		failure.Code = "WORKSPACE_NOT_OWNED"
		failure.Message = "Switchyard does not own this worktree or could not verify its repository identity."
		failure.Retryable = false
		failure.NextAction = "inspect_workspace_ownership"
	case errors.Is(err, workspacecontrol.ErrManagedRequest):
		failure.Code = "WORKSPACE_NOT_ELIGIBLE"
		failure.Message = "The worktree does not satisfy the requested ownership action's safety requirements."
		failure.Retryable = false
		failure.NextAction = "inspect_workspace_eligibility"
	case errors.Is(err, workspacecontrol.ErrManagedExists):
		failure.Code = "WORKSPACE_OWNERSHIP_CONFLICT"
		failure.Message = "A different Switchyard ownership record already claims this worktree identity."
		failure.Retryable = false
		failure.NextAction = "inspect_workspace_ownership"
	case errors.Is(err, workspacecontrol.ErrManagedRecord):
		// Re-adoption is the repair: it re-verifies the worktree's identity and
		// safety and rewrites the private ownership record.
		failure.Code = "WORKSPACE_OWNERSHIP_INVALID"
		failure.Message = "The Switchyard ownership record is invalid or no longer matches the worktree. Adopt the worktree again to re-verify it and rewrite the record."
		failure.Retryable = false
		failure.NextAction = "adopt_worktree"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		failure.Code = "WORKSPACE_ACTION_INTERRUPTED"
		failure.Message = "The workspace action was interrupted before it completed."
		failure.NextAction = "retry"
	}
	return failure
}

func invalidWorkspaceAction() error {
	return &ActionError{Status: 400, Contract: contractv2.ContractError{
		Code: "INVALID_WORKSPACE_ACTION", Message: "The workspace action is invalid.",
	}}
}
