package daemon

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	actioncontrol "github.com/theronburger/switchyard/internal/control/action"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/state"
)

// ProfileActionOperationKind is the operation kind for generic command actions.
// Lifecycle actions dispatch their existing dedicated operations instead.
const ProfileActionOperationKind = "profile.action"

// ProfileActionResolution pins one run to an immutable accepted revision. The
// resolver proves that the repository, action, and every named target exist in
// the accepted configuration; the service owns scope, risk, and dispatch rules.
type ProfileActionResolution struct {
	Definition     actioncontrol.Definition
	Target         actioncontrol.Target
	ProfileKey     string
	ProfileDigest  string
	AcceptedDigest string
	// StartServiceIDs lists the services a lifecycle start dispatches.
	StartServiceIDs []string
}

type ProfileActionResolver interface {
	ListActions(context.Context) (contractv1.ProfileActionList, error)
	ResolveAction(context.Context, contractv1.RunProfileActionRequest) (ProfileActionResolution, error)
	CompileAction(context.Context, ProfileActionResolution, string) (actioncontrol.ExactCommand, error)
}

type ProfileActionRunner interface {
	Run(context.Context, actioncontrol.ExactCommand) (actioncontrol.Outcome, error)
}

type ProfileActionServiceConfig struct {
	Lifecycle   context.Context
	Store       EnvironmentActionStore
	Resolver    ProfileActionResolver
	Runner      ProfileActionRunner
	Environment EnvironmentActions
	Workspace   WorkspaceActions
	NewID       func(string) (string, error)
}

type ProfileActionService struct {
	store       EnvironmentActionStore
	resolver    ProfileActionResolver
	runner      ProfileActionRunner
	environment EnvironmentActions
	workspace   WorkspaceActions
	newID       func(string) (string, error)
	lifecycle   context.Context
	cancel      context.CancelFunc
	mutex       sync.Mutex
	closed      bool
	workers     sync.WaitGroup
}

func NewProfileActionService(config ProfileActionServiceConfig) (*ProfileActionService, error) {
	if config.Lifecycle == nil || config.Store == nil || config.Resolver == nil || config.Runner == nil {
		return nil, errors.New("profile action service dependencies are required")
	}
	if config.NewID == nil {
		config.NewID = randomActionID
	}
	lifecycle, cancel := context.WithCancel(config.Lifecycle)
	return &ProfileActionService{
		store: config.Store, resolver: config.Resolver, runner: config.Runner,
		environment: config.Environment, workspace: config.Workspace, newID: config.NewID,
		lifecycle: lifecycle, cancel: cancel,
	}, nil
}

func (service *ProfileActionService) ListActions(ctx context.Context) (contractv1.ProfileActionList, error) {
	if service == nil {
		return contractv1.ProfileActionList{}, profileActionsUnavailable()
	}
	list, err := service.resolver.ListActions(ctx)
	if err != nil {
		return contractv1.ProfileActionList{}, safeResolutionError(err)
	}
	if list.Actions == nil {
		list.Actions = []contractv1.ProfileAction{}
	}
	list.SchemaVersion = contractv1.SchemaVersion
	if err := list.Validate(); err != nil {
		return contractv1.ProfileActionList{}, profileActionsUnavailable()
	}
	return list, nil
}

func (service *ProfileActionService) RunAction(
	ctx context.Context,
	request contractv1.RunProfileActionRequest,
) (contractv1.MutationReceipt, error) {
	if service == nil {
		return contractv1.MutationReceipt{}, profileActionsUnavailable()
	}
	if request.Validate() != nil {
		return contractv1.MutationReceipt{}, invalidProfileAction()
	}
	resolution, err := service.resolver.ResolveAction(ctx, request)
	if err != nil {
		return contractv1.MutationReceipt{}, safeResolutionError(err)
	}
	definition := resolution.Definition
	if definition.Validate() != nil || definition.ID != request.ActionID || resolution.ProfileKey == "" ||
		resolution.ProfileDigest == "" || resolution.AcceptedDigest == "" {
		return contractv1.MutationReceipt{}, profileActionsUnavailable()
	}
	target := actioncontrol.Target{
		RepositoryID: request.RepositoryID, WorktreeID: request.WorktreeID,
		EnvironmentID: request.EnvironmentID, ServiceID: request.ServiceID,
	}
	if target != resolution.Target {
		return contractv1.MutationReceipt{}, profileActionsUnavailable()
	}
	if err := actioncontrol.ValidateScope(definition.Scope, target); err != nil {
		return contractv1.MutationReceipt{}, &ActionError{Status: http.StatusBadRequest, Contract: contractv1.ContractError{
			Code: "ACTION_SCOPE_MISMATCH", Message: "The action target does not match the action's declared scope.",
			ResourceKind: "action", ResourceID: definition.ID, NextAction: "inspect_action_scope",
		}}
	}
	if definition.RequiresConfirmation() && request.ConfirmedActionID != definition.ID {
		return contractv1.MutationReceipt{}, &ActionError{Status: http.StatusConflict, Contract: contractv1.ContractError{
			Code: "ACTION_CONFIRMATION_REQUIRED", Message: "This action requires explicit confirmation for every run.",
			ResourceKind: "action", ResourceID: definition.ID, NextAction: "confirm_action",
		}}
	}
	if definition.Kind == actioncontrol.KindLifecycle {
		return service.dispatchLifecycle(ctx, request, resolution)
	}
	return service.runCommand(ctx, request, resolution)
}

func (service *ProfileActionService) dispatchLifecycle(
	ctx context.Context,
	request contractv1.RunProfileActionRequest,
	resolution ProfileActionResolution,
) (contractv1.MutationReceipt, error) {
	definition := resolution.Definition
	switch definition.Lifecycle {
	case actioncontrol.LifecyclePrepare:
		if definition.Scope != actioncontrol.ScopeWorktree || service.workspace == nil {
			return contractv1.MutationReceipt{}, lifecycleUnsupported(definition)
		}
		return service.workspace.PrepareWorktree(ctx, contractv1.PrepareWorktreeRequest{
			MutationRequest: request.MutationRequest, WorktreeID: request.WorktreeID,
		})
	case actioncontrol.LifecycleStart:
		if definition.Scope != actioncontrol.ScopeWorktree || service.environment == nil || len(resolution.StartServiceIDs) == 0 {
			return contractv1.MutationReceipt{}, lifecycleUnsupported(definition)
		}
		return service.environment.StartEnvironment(ctx, contractv1.StartEnvironmentRequest{
			MutationRequest: request.MutationRequest, WorktreeID: request.WorktreeID,
			ServiceIDs: append([]string(nil), resolution.StartServiceIDs...),
		})
	case actioncontrol.LifecycleStop:
		if definition.Scope != actioncontrol.ScopeEnvironment || service.environment == nil {
			return contractv1.MutationReceipt{}, lifecycleUnsupported(definition)
		}
		return service.environment.StopEnvironment(ctx, request.EnvironmentID, contractv1.StopEnvironmentRequest{
			MutationRequest: request.MutationRequest,
		})
	case actioncontrol.LifecycleCleanup:
		return contractv1.MutationReceipt{}, &ActionError{Status: http.StatusConflict, Contract: contractv1.ContractError{
			Code: "ACTION_REQUIRES_REVIEW", Message: "Cleanup is an inspectable plan followed by a revision-checked apply; it cannot run as an instant action.",
			ResourceKind: "action", ResourceID: definition.ID, NextAction: "plan_cleanup",
		}}
	default:
		return contractv1.MutationReceipt{}, lifecycleUnsupported(definition)
	}
}

func (service *ProfileActionService) runCommand(
	ctx context.Context,
	request contractv1.RunProfileActionRequest,
	resolution ProfileActionResolution,
) (contractv1.MutationReceipt, error) {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed || service.lifecycle.Err() != nil {
		return contractv1.MutationReceipt{}, profileActionsUnavailable()
	}
	operationID, err := service.newID("operation")
	if err != nil {
		return contractv1.MutationReceipt{}, profileActionsUnavailable()
	}
	command, err := service.resolver.CompileAction(ctx, resolution, operationID)
	if err != nil {
		return contractv1.MutationReceipt{}, safeCompileError(err)
	}
	// The fingerprint binds the idempotency key to the exact target and to the
	// accepted revision, so a replay after re-acceptance is a conflict rather
	// than a silent run of different behavior.
	fingerprint, err := state.FingerprintRequest(struct {
		Request        contractv1.RunProfileActionRequest `json:"request"`
		ProfileDigest  string                             `json:"profileDigest"`
		AcceptedDigest string                             `json:"acceptedDigest"`
	}{Request: request, ProfileDigest: resolution.ProfileDigest, AcceptedDigest: resolution.AcceptedDigest})
	if err != nil {
		return contractv1.MutationReceipt{}, invalidProfileAction()
	}
	operation, created, err := service.store.CreateOperation(ctx, state.NewOperation{
		ID: operationID, RequestID: request.RequestID, IdempotencyKey: request.IdempotencyKey,
		RequestFingerprint: fingerprint, Kind: ProfileActionOperationKind,
		EnvironmentID:               request.EnvironmentID,
		ExpectedEnvironmentRevision: request.ExpectedEnvironmentRevision,
	})
	if err != nil {
		return contractv1.MutationReceipt{}, operationCreationError(err)
	}
	if created {
		service.workers.Add(1)
		go service.execute(operation.ID, resolution, command)
	}
	return receiptForOperation(request.RequestID, operation), nil
}

func (service *ProfileActionService) execute(operationID string, resolution ProfileActionResolution, command actioncontrol.ExactCommand) {
	defer service.workers.Done()
	actionID := resolution.Definition.ID
	logReference := filepath.ToSlash(filepath.Join(resolution.ProfileKey, operationID))
	if _, err := service.store.TransitionOperation(service.lifecycle, operationID, string(domain.OperationRunning), nil); err != nil {
		service.fail(operationID, actionFailure(actionID, logReference, err, nil))
		return
	}
	outcome, err := service.runner.Run(service.lifecycle, command)
	if err != nil {
		service.fail(operationID, actionFailure(actionID, logReference, err, nil))
		return
	}
	if outcome.TimedOut || outcome.ExitCode != 0 {
		service.fail(operationID, actionFailure(actionID, logReference, nil, &outcome))
		return
	}
	if _, err := service.store.TransitionOperation(service.lifecycle, operationID, string(domain.OperationSucceeded), nil); err != nil {
		service.fail(operationID, actionFailure(actionID, logReference, err, nil))
	}
}

func (service *ProfileActionService) fail(operationID string, failure contractv1.ContractError) {
	ctx, cancel := context.WithTimeout(context.Background(), actionFinalizationTimeout)
	defer cancel()
	_, _ = service.store.TransitionOperation(ctx, operationID, string(domain.OperationFailed), &failure)
}

func (service *ProfileActionService) CloseAndWait(ctx context.Context) error {
	if service == nil {
		return nil
	}
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

// actionFailure builds a bounded failure. It deliberately carries no
// executable, argument, environment, or output content; the log reference
// points at Switchyard-owned bounded files.
func actionFailure(actionID, logReference string, err error, outcome *actioncontrol.Outcome) contractv1.ContractError {
	failure := contractv1.ContractError{
		Code: "ACTION_FAILED", Message: "The profile action could not be completed.", Retryable: true,
		ResourceKind: "action", ResourceID: actionID, LogReference: logReference, NextAction: "inspect_operation_diagnostics",
	}
	switch {
	case outcome != nil && outcome.TimedOut:
		failure.Code = "ACTION_TIMED_OUT"
		failure.Message = "The profile action exceeded its accepted timeout and was stopped."
		failure.Diagnostic = "The command's process group received TERM and then KILL."
	case outcome != nil:
		exitCode := outcome.ExitCode
		failure.Code = "ACTION_COMMAND_FAILED"
		failure.Message = "The profile action command exited unsuccessfully."
		failure.Retryable = false
		failure.ExitCode = &exitCode
		if outcome.StdoutTruncated || outcome.StderrTruncated {
			failure.Diagnostic = "Captured output was truncated at the bounded limit."
		}
	case errors.Is(err, actioncontrol.ErrInvalidCommand):
		failure.Code = "ACTION_COMMAND_INVALID"
		failure.Message = "The accepted command no longer resolves to a safe executable invocation."
		failure.Retryable = false
		failure.NextAction = "revalidate_configuration"
	case errors.Is(err, actioncontrol.ErrCommandStart):
		failure.Code = "ACTION_COMMAND_START_FAILED"
		failure.Message = "The profile action command could not be started."
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		failure.Code = "ACTION_INTERRUPTED"
		failure.Message = "The profile action was interrupted before it completed."
		failure.NextAction = "retry"
	}
	return failure
}

func lifecycleUnsupported(definition actioncontrol.Definition) error {
	return &ActionError{Status: http.StatusConflict, Contract: contractv1.ContractError{
		Code: "ACTION_LIFECYCLE_UNSUPPORTED", Message: "The lifecycle action cannot be dispatched at its declared scope.",
		ResourceKind: "action", ResourceID: definition.ID, NextAction: "inspect_action_scope",
	}}
}

func safeCompileError(err error) error {
	var actionError *ActionError
	if errors.As(err, &actionError) && validActionError(actionError) {
		return actionError
	}
	return &ActionError{Status: http.StatusConflict, Contract: contractv1.ContractError{
		Code: "ACTION_NOT_COMPILABLE", Message: "The accepted action could not be compiled into an exact command for this target.",
		NextAction: "inspect_action_scope",
	}}
}

func invalidProfileAction() error {
	return &ActionError{Status: http.StatusBadRequest, Contract: contractv1.ContractError{
		Code: "INVALID_ACTION_REQUEST", Message: "The profile action request is invalid.",
	}}
}

func profileActionsUnavailable() error {
	return &ActionError{Status: http.StatusServiceUnavailable, Contract: contractv1.ContractError{
		Code: "ACTIONS_UNAVAILABLE", Message: "Profile actions are temporarily unavailable.", Retryable: true,
	}}
}
