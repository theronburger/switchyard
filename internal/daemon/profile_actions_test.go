package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	actioncontrol "github.com/theronburger/switchyard/internal/control/action"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/state"
)

type fakeProfileActionResolver struct {
	definitions    map[string]actioncontrol.Definition
	acceptedDigest string
	resolveErr     error
	compileErr     error
	compiled       atomic.Int32
}

func newFakeProfileActionResolver() *fakeProfileActionResolver {
	return &fakeProfileActionResolver{
		acceptedDigest: "sha256:" + strings.Repeat("a", 64),
		definitions: map[string]actioncontrol.Definition{
			"tidy":  {ID: "tidy", DisplayName: "Tidy", Scope: "worktree", Risk: "local", Kind: "command"},
			"probe": {ID: "probe", DisplayName: "Probe", Scope: "service", Risk: "remote-read", Kind: "command"},
			"push":  {ID: "push", DisplayName: "Push", Scope: "repository", Risk: "remote-write", Kind: "command"},
			"warm":  {ID: "warm", DisplayName: "Prepare", Scope: "worktree", Risk: "local", Kind: "lifecycle", Lifecycle: "prepare"},
			"up":    {ID: "up", DisplayName: "Start", Scope: "worktree", Risk: "local", Kind: "lifecycle", Lifecycle: "start"},
			"down":  {ID: "down", DisplayName: "Stop", Scope: "environment", Risk: "local", Kind: "lifecycle", Lifecycle: "stop"},
			"sweep": {ID: "sweep", DisplayName: "Cleanup", Scope: "repository", Risk: "local", Kind: "lifecycle", Lifecycle: "cleanup"},
		},
	}
}

func (resolver *fakeProfileActionResolver) ListActions(context.Context) (contractv1.ProfileActionList, error) {
	list := contractv1.ProfileActionList{AcceptedDigest: resolver.acceptedDigest, Actions: []contractv1.ProfileAction{}}
	for _, definition := range resolver.definitions {
		list.Actions = append(list.Actions, contractv1.ProfileAction{
			ID: definition.ID, RepositoryID: "repository_01", ProfileKey: "sample", ProfileDigest: resolver.acceptedDigest,
			DisplayName: definition.DisplayName, Scope: definition.Scope, Risk: definition.Risk, Kind: definition.Kind,
			Lifecycle: definition.Lifecycle, RequiresConfirmation: definition.RequiresConfirmation(),
		})
	}
	return list, nil
}

func (resolver *fakeProfileActionResolver) ResolveAction(_ context.Context, request contractv1.RunProfileActionRequest) (ProfileActionResolution, error) {
	if resolver.resolveErr != nil {
		return ProfileActionResolution{}, resolver.resolveErr
	}
	definition, found := resolver.definitions[request.ActionID]
	if !found || request.RepositoryID != "repository_01" {
		return ProfileActionResolution{}, &ActionError{Status: http.StatusNotFound, Contract: contractv1.ContractError{Code: "ACTION_NOT_FOUND", Message: "missing"}}
	}
	return ProfileActionResolution{
		Definition: definition, ProfileKey: "sample", ProfileDigest: resolver.acceptedDigest, AcceptedDigest: resolver.acceptedDigest,
		Target: actioncontrol.Target{
			RepositoryID: request.RepositoryID, WorktreeID: request.WorktreeID,
			EnvironmentID: request.EnvironmentID, ServiceID: request.ServiceID,
		},
		StartServiceIDs: []string{"web"},
	}, nil
}

func (resolver *fakeProfileActionResolver) CompileAction(_ context.Context, resolution ProfileActionResolution, operationID string) (actioncontrol.ExactCommand, error) {
	if resolver.compileErr != nil {
		return actioncontrol.ExactCommand{}, resolver.compileErr
	}
	resolver.compiled.Add(1)
	return actioncontrol.ExactCommand{
		Executable: "/usr/bin/true", Arguments: []string{"--secret-arg", "hunter2"}, Directory: "/tmp",
		Environment: []string{"HOME=/tmp", "PATH=/usr/bin", "TMPDIR=/tmp", "API_TOKEN=hunter2"}, Timeout: time.Minute,
		RunDirectory: "/tmp/actions/sample/" + operationID,
	}, nil
}

type fakeProfileActionRunner struct {
	run   func(actioncontrol.ExactCommand) (actioncontrol.Outcome, error)
	calls atomic.Int32
}

func (runner *fakeProfileActionRunner) Run(_ context.Context, command actioncontrol.ExactCommand) (actioncontrol.Outcome, error) {
	runner.calls.Add(1)
	if runner.run == nil {
		return actioncontrol.Outcome{ExitCode: 0}, nil
	}
	return runner.run(command)
}

type fakeLifecycleActions struct {
	starts   []contractv1.StartEnvironmentRequest
	stops    []string
	prepares []contractv1.PrepareWorktreeRequest
}

func (actions *fakeLifecycleActions) StartEnvironment(_ context.Context, request contractv1.StartEnvironmentRequest) (contractv1.MutationReceipt, error) {
	actions.starts = append(actions.starts, request)
	return contractv1.MutationReceipt{SchemaVersion: 1, RequestID: request.RequestID, OperationID: "operation_start", RunID: "run_01", AcceptedAt: time.Now(), EnvironmentID: "environment_01"}, nil
}

func (actions *fakeLifecycleActions) StopEnvironment(_ context.Context, environmentID string, request contractv1.StopEnvironmentRequest) (contractv1.MutationReceipt, error) {
	actions.stops = append(actions.stops, environmentID)
	return contractv1.MutationReceipt{SchemaVersion: 1, RequestID: request.RequestID, OperationID: "operation_stop", AcceptedAt: time.Now(), EnvironmentID: environmentID}, nil
}

func (actions *fakeLifecycleActions) PrepareWorktree(_ context.Context, request contractv1.PrepareWorktreeRequest) (contractv1.MutationReceipt, error) {
	actions.prepares = append(actions.prepares, request)
	return contractv1.MutationReceipt{SchemaVersion: 1, RequestID: request.RequestID, OperationID: "operation_prepare", AcceptedAt: time.Now()}, nil
}

func (*fakeLifecycleActions) CreateWorktree(context.Context, contractv1.CreateWorktreeRequest) (contractv1.MutationReceipt, error) {
	return contractv1.MutationReceipt{}, errors.New("unexpected")
}

func (*fakeLifecycleActions) AdoptWorktree(context.Context, contractv1.AdoptWorktreeRequest) (contractv1.MutationReceipt, error) {
	return contractv1.MutationReceipt{}, errors.New("unexpected")
}

func (*fakeLifecycleActions) ArchiveWorktree(context.Context, contractv1.ArchiveWorktreeRequest) (contractv1.MutationReceipt, error) {
	return contractv1.MutationReceipt{}, errors.New("unexpected")
}

func actionRequest(actionID string) contractv1.RunProfileActionRequest {
	return contractv1.RunProfileActionRequest{
		MutationRequest: contractv1.MutationRequest{SchemaVersion: contractv1.SchemaVersion, RequestID: "request_01", IdempotencyKey: "key_" + actionID},
		RepositoryID:    "repository_01", ActionID: actionID,
	}
}

func newProfileActionService(t *testing.T, store *fakeActionOperationStore, resolver *fakeProfileActionResolver, runner *fakeProfileActionRunner, lifecycle *fakeLifecycleActions) *ProfileActionService {
	t.Helper()
	var counter atomic.Int32
	config := ProfileActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Resolver: resolver, Runner: runner,
		NewID: func(prefix string) (string, error) {
			return prefix + "_" + strings.Repeat("0", 2) + string(rune('a'+counter.Add(1))), nil
		},
	}
	if lifecycle != nil {
		config.Environment = lifecycle
		config.Workspace = lifecycle
	}
	service, err := NewProfileActionService(config)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func closeService(t *testing.T, service *ProfileActionService) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := service.CloseAndWait(ctx); err != nil {
		t.Fatal(err)
	}
}

func actionErrorCode(t *testing.T, err error) (int, string) {
	t.Helper()
	var actionError *ActionError
	if !errors.As(err, &actionError) {
		t.Fatalf("expected ActionError, got %v", err)
	}
	return actionError.Status, actionError.Contract.Code
}

func TestProfileActionServiceRunsCommandAndKeepsSecretsOutOfState(t *testing.T) {
	store := newFakeActionOperationStore()
	resolver := newFakeProfileActionResolver()
	runner := &fakeProfileActionRunner{}
	service := newProfileActionService(t, store, resolver, runner, nil)
	request := actionRequest("tidy")
	request.WorktreeID = "worktree_01"
	receipt, err := service.RunAction(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.RunAction(context.Background(), request)
	if err != nil || replay.OperationID != receipt.OperationID {
		t.Fatalf("idempotent replay: %+v %v", replay, err)
	}
	closeService(t, service)
	operation, err := store.ReadOperation(context.Background(), receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls.Load() != 1 || operation.State != string(domain.OperationSucceeded) || operation.Kind != ProfileActionOperationKind ||
		operation.EnvironmentID != "" || receipt.EnvironmentID != "" {
		t.Fatalf("command action: calls=%d operation=%+v", runner.calls.Load(), operation)
	}
}

func TestProfileActionServiceBindsIdempotencyToAcceptedRevision(t *testing.T) {
	store := newFakeActionOperationStore()
	resolver := newFakeProfileActionResolver()
	service := newProfileActionService(t, store, resolver, &fakeProfileActionRunner{}, nil)
	request := actionRequest("tidy")
	request.WorktreeID = "worktree_01"
	if _, err := service.RunAction(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	resolver.acceptedDigest = "sha256:" + strings.Repeat("b", 64)
	_, err := service.RunAction(context.Background(), request)
	if status, code := actionErrorCode(t, err); status != http.StatusConflict || code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("replay across revisions: status=%d code=%s", status, code)
	}
	closeService(t, service)
}

func TestProfileActionServiceRejectsScopeMismatchAndMissingConfirmation(t *testing.T) {
	store := newFakeActionOperationStore()
	resolver := newFakeProfileActionResolver()
	runner := &fakeProfileActionRunner{}
	service := newProfileActionService(t, store, resolver, runner, nil)
	defer closeService(t, service)
	cases := []struct {
		name   string
		mutate func(*contractv1.RunProfileActionRequest)
		status int
		code   string
	}{
		{"worktree action without worktree", func(r *contractv1.RunProfileActionRequest) { r.ActionID = "tidy" }, http.StatusBadRequest, "ACTION_SCOPE_MISMATCH"},
		{"worktree action aimed at environment", func(r *contractv1.RunProfileActionRequest) { r.ActionID = "tidy"; r.EnvironmentID = "environment_01" }, http.StatusBadRequest, "ACTION_SCOPE_MISMATCH"},
		{"service action without service", func(r *contractv1.RunProfileActionRequest) { r.ActionID = "probe"; r.EnvironmentID = "environment_01" }, http.StatusBadRequest, "ACTION_SCOPE_MISMATCH"},
		{"repository action with worktree", func(r *contractv1.RunProfileActionRequest) {
			r.ActionID = "push"
			r.WorktreeID = "worktree_01"
			r.ConfirmedActionID = "push"
		}, http.StatusBadRequest, "ACTION_SCOPE_MISMATCH"},
		{"remote-write without confirmation", func(r *contractv1.RunProfileActionRequest) { r.ActionID = "push" }, http.StatusConflict, "ACTION_CONFIRMATION_REQUIRED"},
		{"unknown action", func(r *contractv1.RunProfileActionRequest) { r.ActionID = "nope" }, http.StatusNotFound, "ACTION_NOT_FOUND"},
		{"cleanup lifecycle needs review", func(r *contractv1.RunProfileActionRequest) { r.ActionID = "sweep" }, http.StatusConflict, "ACTION_REQUIRES_REVIEW"},
		{"lifecycle without dispatcher", func(r *contractv1.RunProfileActionRequest) { r.ActionID = "warm"; r.WorktreeID = "worktree_01" }, http.StatusConflict, "ACTION_LIFECYCLE_UNSUPPORTED"},
	}
	for _, c := range cases {
		request := actionRequest("tidy")
		c.mutate(&request)
		_, err := service.RunAction(context.Background(), request)
		if status, code := actionErrorCode(t, err); status != c.status || code != c.code {
			t.Fatalf("%s: status=%d code=%s (want %d %s)", c.name, status, code, c.status, c.code)
		}
	}
	if runner.calls.Load() != 0 || resolver.compiled.Load() != 0 || len(store.operations) != 0 {
		t.Fatal("a rejected action was compiled, persisted, or executed")
	}
	// A confirmed remote-write action is accepted and executed.
	request := actionRequest("push")
	request.ConfirmedActionID = "push"
	if _, err := service.RunAction(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func TestProfileActionServiceFailsClosedWhenConfigurationIsNotAccepted(t *testing.T) {
	store := newFakeActionOperationStore()
	resolver := newFakeProfileActionResolver()
	resolver.resolveErr = &ActionError{Status: http.StatusConflict, Contract: contractv1.ContractError{Code: "CONFIGURATION_NOT_ACCEPTED", Message: "accept first"}}
	runner := &fakeProfileActionRunner{}
	service := newProfileActionService(t, store, resolver, runner, nil)
	defer closeService(t, service)
	request := actionRequest("tidy")
	request.WorktreeID = "worktree_01"
	_, err := service.RunAction(context.Background(), request)
	if status, code := actionErrorCode(t, err); status != http.StatusConflict || code != "CONFIGURATION_NOT_ACCEPTED" {
		t.Fatalf("status=%d code=%s", status, code)
	}
	// A resolver fault that is not a safe contract error is reported generically.
	resolver.resolveErr = errors.New("sqlite: /Users/person/Library/state.sqlite is locked")
	_, err = service.RunAction(context.Background(), request)
	var actionError *ActionError
	if !errors.As(err, &actionError) || strings.Contains(actionError.Contract.Message, "/Users/") {
		t.Fatalf("unsafe resolver error leaked: %v", err)
	}
	if runner.calls.Load() != 0 || len(store.operations) != 0 {
		t.Fatal("a rejected action was persisted or executed")
	}
}

func TestProfileActionServiceRecordsBoundedCommandFailures(t *testing.T) {
	cases := []struct {
		name      string
		run       func(actioncontrol.ExactCommand) (actioncontrol.Outcome, error)
		code      string
		exitCode  *int
		retryable bool
	}{
		{"exit status", func(actioncontrol.ExactCommand) (actioncontrol.Outcome, error) {
			return actioncontrol.Outcome{ExitCode: 7, StdoutTruncated: true}, nil
		}, "ACTION_COMMAND_FAILED", intPointer(7), false},
		{"timeout", func(actioncontrol.ExactCommand) (actioncontrol.Outcome, error) {
			return actioncontrol.Outcome{ExitCode: 143, TimedOut: true}, nil
		}, "ACTION_TIMED_OUT", nil, true},
		{"invalid command", func(actioncontrol.ExactCommand) (actioncontrol.Outcome, error) {
			return actioncontrol.Outcome{}, actioncontrol.ErrInvalidCommand
		}, "ACTION_COMMAND_INVALID", nil, false},
		{"start failure", func(actioncontrol.ExactCommand) (actioncontrol.Outcome, error) {
			return actioncontrol.Outcome{}, errors.Join(actioncontrol.ErrCommandStart, errors.New("fork /usr/bin/true --secret-arg hunter2"))
		}, "ACTION_COMMAND_START_FAILED", nil, true},
		{"interrupted", func(actioncontrol.ExactCommand) (actioncontrol.Outcome, error) {
			return actioncontrol.Outcome{}, context.Canceled
		}, "ACTION_INTERRUPTED", nil, true},
	}
	for _, c := range cases {
		store := newFakeActionOperationStore()
		resolver := newFakeProfileActionResolver()
		runner := &fakeProfileActionRunner{run: c.run}
		service := newProfileActionService(t, store, resolver, runner, nil)
		request := actionRequest("probe")
		request.EnvironmentID = "environment_01"
		request.ServiceID = "web"
		receipt, err := service.RunAction(context.Background(), request)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		closeService(t, service)
		operation, err := store.ReadOperation(context.Background(), receipt.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if operation.State != string(domain.OperationFailed) || operation.Error == nil || operation.Error.Code != c.code ||
			operation.Error.Retryable != c.retryable || operation.Error.ResourceKind != "action" || operation.Error.ResourceID != "probe" ||
			operation.Error.LogReference != "sample/"+receipt.OperationID || operation.EnvironmentID != "environment_01" {
			t.Fatalf("%s: operation=%+v error=%+v", c.name, operation, operation.Error)
		}
		if (c.exitCode == nil) != (operation.Error.ExitCode == nil) || (c.exitCode != nil && *c.exitCode != *operation.Error.ExitCode) {
			t.Fatalf("%s: exit code %v", c.name, operation.Error.ExitCode)
		}
		serialized := strings.Join([]string{operation.Error.Message, operation.Error.Diagnostic, operation.Error.NextAction, operation.Error.Step}, " ")
		if strings.Contains(serialized, "hunter2") || strings.Contains(serialized, "/usr/bin/true") || strings.Contains(serialized, "API_TOKEN") {
			t.Fatalf("%s: command or secret leaked into operation state: %q", c.name, serialized)
		}
	}
}

func TestProfileActionServiceSerializesEnvironmentScopedActions(t *testing.T) {
	store := newFakeActionOperationStore()
	store.createErr = state.ErrEnvironmentBusy
	service := newProfileActionService(t, store, newFakeProfileActionResolver(), &fakeProfileActionRunner{}, nil)
	defer closeService(t, service)
	request := actionRequest("probe")
	request.EnvironmentID = "environment_01"
	request.ServiceID = "web"
	_, err := service.RunAction(context.Background(), request)
	if status, code := actionErrorCode(t, err); status != http.StatusConflict || code != "ENVIRONMENT_BUSY" {
		t.Fatalf("status=%d code=%s", status, code)
	}
}

func TestProfileActionServiceDispatchesLifecycleActions(t *testing.T) {
	store := newFakeActionOperationStore()
	lifecycle := &fakeLifecycleActions{}
	runner := &fakeProfileActionRunner{}
	service := newProfileActionService(t, store, newFakeProfileActionResolver(), runner, lifecycle)
	defer closeService(t, service)

	prepare := actionRequest("warm")
	prepare.WorktreeID = "worktree_01"
	receipt, err := service.RunAction(context.Background(), prepare)
	if err != nil || receipt.OperationID != "operation_prepare" || len(lifecycle.prepares) != 1 ||
		lifecycle.prepares[0].WorktreeID != "worktree_01" || lifecycle.prepares[0].IdempotencyKey != "key_warm" {
		t.Fatalf("prepare dispatch: %+v %v %+v", receipt, err, lifecycle.prepares)
	}
	start := actionRequest("up")
	start.WorktreeID = "worktree_01"
	receipt, err = service.RunAction(context.Background(), start)
	if err != nil || receipt.OperationID != "operation_start" || len(lifecycle.starts) != 1 ||
		strings.Join(lifecycle.starts[0].ServiceIDs, ",") != "web" || lifecycle.starts[0].TargetID != "" {
		t.Fatalf("start dispatch: %+v %v %+v", receipt, err, lifecycle.starts)
	}
	stop := actionRequest("down")
	stop.EnvironmentID = "environment_01"
	receipt, err = service.RunAction(context.Background(), stop)
	if err != nil || receipt.OperationID != "operation_stop" || len(lifecycle.stops) != 1 || lifecycle.stops[0] != "environment_01" {
		t.Fatalf("stop dispatch: %+v %v %+v", receipt, err, lifecycle.stops)
	}
	if runner.calls.Load() != 0 || len(store.operations) != 0 {
		t.Fatal("lifecycle actions must not run through the generic command runner")
	}
}

func TestProfileActionServiceRefusesWorkAfterClose(t *testing.T) {
	store := newFakeActionOperationStore()
	runner := &fakeProfileActionRunner{}
	service := newProfileActionService(t, store, newFakeProfileActionResolver(), runner, nil)
	closeService(t, service)
	request := actionRequest("tidy")
	request.WorktreeID = "worktree_01"
	_, err := service.RunAction(context.Background(), request)
	if status, code := actionErrorCode(t, err); status != http.StatusServiceUnavailable || code != "ACTIONS_UNAVAILABLE" {
		t.Fatalf("status=%d code=%s", status, code)
	}
	var nilService *ProfileActionService
	if _, err := nilService.ListActions(context.Background()); err == nil {
		t.Fatal("nil service must report unavailability, not panic")
	}
}

func TestProfileActionServiceListsAcceptedActions(t *testing.T) {
	service := newProfileActionService(t, newFakeActionOperationStore(), newFakeProfileActionResolver(), &fakeProfileActionRunner{}, nil)
	defer closeService(t, service)
	list, err := service.ListActions(context.Background())
	if err != nil || list.SchemaVersion != contractv1.SchemaVersion || len(list.Actions) != 7 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if err := list.Validate(); err != nil {
		t.Fatal(err)
	}
}

func intPointer(value int) *int { return &value }
