package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/state"
)

type fakeActionOperationStore struct {
	mutex       sync.Mutex
	operations  map[string]contractv2.Operation
	byKey       map[string]state.NewOperation
	createErr   error
	transitions int
	transition  func(context.Context, string, string, *contractv2.ContractError) (contractv2.Operation, error)
}

func newFakeActionOperationStore() *fakeActionOperationStore {
	return &fakeActionOperationStore{
		operations: make(map[string]contractv2.Operation),
		byKey:      make(map[string]state.NewOperation),
	}
}

func (store *fakeActionOperationStore) CreateOperation(
	_ context.Context,
	request state.NewOperation,
) (contractv2.Operation, bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.createErr != nil {
		return contractv2.Operation{}, false, store.createErr
	}
	if existing, found := store.byKey[request.IdempotencyKey]; found {
		if existing.RequestFingerprint != request.RequestFingerprint {
			return contractv2.Operation{}, false, state.ErrIdempotencyConflict
		}
		return store.operations[existing.ID], false, nil
	}
	now := time.Date(2026, 8, 14, 16, 30, 0, 0, time.UTC)
	operation := contractv2.Operation{
		ID: request.ID, RunID: request.RunID, Kind: request.Kind, State: string(domain.OperationPending),
		EnvironmentID: request.EnvironmentID, CreatedAt: now, UpdatedAt: now,
	}
	store.byKey[request.IdempotencyKey] = request
	store.operations[request.ID] = operation
	return operation, true, nil
}

func (store *fakeActionOperationStore) ReadOperation(
	_ context.Context,
	operationID string,
) (contractv2.Operation, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	operation, found := store.operations[operationID]
	if !found {
		return contractv2.Operation{}, state.ErrOperationNotFound
	}
	return operation, nil
}

func (store *fakeActionOperationStore) TransitionOperation(
	ctx context.Context,
	operationID string,
	nextState string,
	failure *contractv2.ContractError,
) (contractv2.Operation, error) {
	if store.transition != nil {
		return store.transition(ctx, operationID, nextState, failure)
	}
	store.mutex.Lock()
	defer store.mutex.Unlock()
	operation := store.operations[operationID]
	operation.State = nextState
	operation.Error = failure
	store.operations[operationID] = operation
	store.transitions++
	return operation, nil
}

type fakeActionJournal struct {
	records []environmentcontrol.OperationRecord
	err     error
}

func (journal fakeActionJournal) Incomplete(context.Context) ([]environmentcontrol.OperationRecord, error) {
	return append([]environmentcontrol.OperationRecord(nil), journal.records...), journal.err
}

type fakeActionCoordinator struct {
	start func(context.Context, environmentcontrol.StartRequest) (environmentcontrol.EnvironmentResult, error)
	stop  func(context.Context, environmentcontrol.StopRequest) (environmentcontrol.EnvironmentResult, error)
}

func (coordinator fakeActionCoordinator) Start(
	ctx context.Context,
	request environmentcontrol.StartRequest,
) (environmentcontrol.EnvironmentResult, error) {
	return coordinator.start(ctx, request)
}

func (coordinator fakeActionCoordinator) Stop(
	ctx context.Context,
	request environmentcontrol.StopRequest,
) (environmentcontrol.EnvironmentResult, error) {
	return coordinator.stop(ctx, request)
}

type fakeActionResolver struct {
	start EnvironmentStartResolution
	err   error
}

type fakeWorkspaceEnsurer struct {
	ensure func(context.Context, workspacecontrol.EnsureRequest) (workspacecontrol.Result, error)
}

func (ensurer fakeWorkspaceEnsurer) Ensure(
	ctx context.Context,
	request workspacecontrol.EnsureRequest,
) (workspacecontrol.Result, error) {
	return ensurer.ensure(ctx, request)
}

func TestEnvironmentActionServiceEnsuresWorkspaceBeforeEnvironment(t *testing.T) {
	store := newFakeActionOperationStore()
	order := make([]string, 0, 2)
	var orderMutex sync.Mutex
	coordinator := fakeActionCoordinator{
		start: func(_ context.Context, request environmentcontrol.StartRequest) (environmentcontrol.EnvironmentResult, error) {
			orderMutex.Lock()
			defer orderMutex.Unlock()
			order = append(order, "environment")
			return environmentcontrol.EnvironmentResult{}, nil
		},
		stop: func(context.Context, environmentcontrol.StopRequest) (environmentcontrol.EnvironmentResult, error) {
			return environmentcontrol.EnvironmentResult{}, nil
		},
	}
	service, err := NewEnvironmentActionService(EnvironmentActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Journal: fakeActionJournal{}, Coordinator: coordinator,
		Workspace: fakeWorkspaceEnsurer{ensure: func(
			_ context.Context,
			request workspacecontrol.EnsureRequest,
		) (workspacecontrol.Result, error) {
			if request.OperationID != "operation_1" || request.WorktreeID != "worktree_01" {
				t.Errorf("workspace request: %+v", request)
			}
			orderMutex.Lock()
			defer orderMutex.Unlock()
			order = append(order, "workspace")
			return workspacecontrol.Result{}, nil
		}},
		Resolver: fakeActionResolver{start: EnvironmentStartResolution{
			EnvironmentID: "environment_01", WorktreeID: "worktree_01",
			Intent: environmentcontrol.PlanIntent{ProfileDigest: "sample", ServiceIDs: []string{"storefront"}},
		}},
		NewID: func(prefix string) (string, error) {
			if prefix == "operation" {
				return "operation_1", nil
			}
			return "run_2", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartEnvironment(context.Background(), validActionStartRequest()); err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	orderMutex.Lock()
	defer orderMutex.Unlock()
	if len(order) != 2 || order[0] != "workspace" || order[1] != "environment" {
		t.Fatalf("action order: %v", order)
	}
}

func TestEnvironmentActionServiceDoesNotStartEnvironmentWhenWorkspaceFails(t *testing.T) {
	store := newFakeActionOperationStore()
	coordinatorCalls := 0
	coordinator := fakeActionCoordinator{
		start: func(context.Context, environmentcontrol.StartRequest) (environmentcontrol.EnvironmentResult, error) {
			coordinatorCalls++
			return environmentcontrol.EnvironmentResult{}, nil
		},
		stop: func(context.Context, environmentcontrol.StopRequest) (environmentcontrol.EnvironmentResult, error) {
			return environmentcontrol.EnvironmentResult{}, nil
		},
	}
	service, err := NewEnvironmentActionService(EnvironmentActionServiceConfig{
		Lifecycle: context.Background(), Store: store, Journal: fakeActionJournal{}, Coordinator: coordinator,
		Workspace: fakeWorkspaceEnsurer{ensure: func(
			context.Context,
			workspacecontrol.EnsureRequest,
		) (workspacecontrol.Result, error) {
			return workspacecontrol.Result{}, errors.New("private hydration path")
		}},
		Resolver: fakeActionResolver{start: EnvironmentStartResolution{
			EnvironmentID: "environment_01", WorktreeID: "worktree_01",
			Intent: environmentcontrol.PlanIntent{ProfileDigest: "sample", ServiceIDs: []string{"storefront"}},
		}},
		NewID: func(prefix string) (string, error) {
			if prefix == "operation" {
				return "operation_1", nil
			}
			return "run_2", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartEnvironment(context.Background(), validActionStartRequest()); err != nil {
		t.Fatal(err)
	}
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	operation, err := store.ReadOperation(context.Background(), "operation_1")
	if err != nil {
		t.Fatal(err)
	}
	if coordinatorCalls != 0 || operation.State != string(domain.OperationFailed) ||
		operation.Error == nil || operation.Error.Code != "ENVIRONMENT_ACTION_FAILED" {
		t.Fatalf("workspace failure: calls=%d operation=%+v", coordinatorCalls, operation)
	}
}

func (resolver fakeActionResolver) ResolveStart(
	context.Context,
	contractv2.StartEnvironmentRequest,
) (EnvironmentStartResolution, error) {
	if resolver.start.Source.Revision == "" {
		resolver.start.Source = environmentcontrol.SourceSnapshot{
			Revision:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ObservedAt: time.Date(2026, 8, 14, 16, 29, 0, 0, time.UTC),
		}
	}
	return resolver.start, resolver.err
}

func (resolver fakeActionResolver) ResolveStop(
	_ context.Context,
	environmentID string,
	_ contractv2.StopEnvironmentRequest,
) error {
	if resolver.err != nil {
		return resolver.err
	}
	if environmentID != resolver.start.EnvironmentID {
		return invalidActionRequest()
	}
	return nil
}

func TestEnvironmentActionServiceIsIdempotentAndDetachesWorkerFromRequest(t *testing.T) {
	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	defer cancelLifecycle()
	store := newFakeActionOperationStore()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	coordinator := fakeActionCoordinator{
		start: func(ctx context.Context, request environmentcontrol.StartRequest) (environmentcontrol.EnvironmentResult, error) {
			if request.OperationID != "operation_1" || request.RunID != "run_2" ||
				request.EnvironmentID != "environment_01" || request.Intent == nil {
				t.Errorf("start request: %+v", request)
			}
			started <- ctx
			<-release
			return environmentcontrol.EnvironmentResult{}, nil
		},
		stop: func(context.Context, environmentcontrol.StopRequest) (environmentcontrol.EnvironmentResult, error) {
			return environmentcontrol.EnvironmentResult{}, nil
		},
	}
	service := newTestActionService(t, lifecycle, store, fakeActionJournal{}, coordinator)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	request := validActionStartRequest()
	receipt, err := service.StartEnvironment(requestContext, request)
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest()
	workerContext := <-started
	select {
	case <-workerContext.Done():
		t.Fatal("HTTP request cancellation propagated into the accepted worker")
	default:
	}
	retried, err := service.StartEnvironment(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if retried.OperationID != receipt.OperationID || receipt.RunID != "run_2" ||
		retried.RunID != receipt.RunID || receipt.Validate() != nil {
		t.Fatalf("receipt=%+v retried=%+v", receipt, retried)
	}
	close(release)
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentActionServiceCloseCancelsAndFinalizesUnstartedOperation(t *testing.T) {
	store := newFakeActionOperationStore()
	coordinatorStarted := make(chan struct{})
	coordinator := fakeActionCoordinator{
		start: func(ctx context.Context, _ environmentcontrol.StartRequest) (environmentcontrol.EnvironmentResult, error) {
			close(coordinatorStarted)
			<-ctx.Done()
			return environmentcontrol.EnvironmentResult{}, ctx.Err()
		},
		stop: func(context.Context, environmentcontrol.StopRequest) (environmentcontrol.EnvironmentResult, error) {
			return environmentcontrol.EnvironmentResult{}, nil
		},
	}
	service := newTestActionService(t, context.Background(), store, fakeActionJournal{}, coordinator)
	receipt, err := service.StartEnvironment(context.Background(), validActionStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	<-coordinatorStarted
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
		operation.Error.Code != "ENVIRONMENT_ACTION_FAILED" {
		t.Fatalf("finalized operation: %+v", operation)
	}
	if _, err := service.StartEnvironment(context.Background(), validActionStartRequest()); err == nil {
		t.Fatal("closed action service accepted a mutation")
	}
}

func TestEnvironmentActionServicePreservesIncompleteJournalForRestartRecovery(t *testing.T) {
	store := newFakeActionOperationStore()
	coordinator := fakeActionCoordinator{
		start: func(context.Context, environmentcontrol.StartRequest) (environmentcontrol.EnvironmentResult, error) {
			return environmentcontrol.EnvironmentResult{}, errors.New("private coordinator failure")
		},
		stop: func(context.Context, environmentcontrol.StopRequest) (environmentcontrol.EnvironmentResult, error) {
			return environmentcontrol.EnvironmentResult{}, nil
		},
	}
	journal := fakeActionJournal{records: []environmentcontrol.OperationRecord{{ID: "operation_1"}}}
	service := newTestActionService(t, context.Background(), store, journal, coordinator)
	if _, err := service.StartEnvironment(context.Background(), validActionStartRequest()); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), time.Second)
	defer cancelWait()
	if err := service.CloseAndWait(waitContext); err != nil {
		t.Fatal(err)
	}
	if store.transitions != 0 {
		t.Fatalf("action service overrode %d journal-owned operations", store.transitions)
	}
}

func TestEnvironmentActionServiceMapsPersistenceConflicts(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{err: state.ErrIdempotencyConflict, code: "IDEMPOTENCY_CONFLICT"},
		{err: state.ErrEnvironmentBusy, code: "ENVIRONMENT_BUSY"},
		{err: state.ErrEnvironmentRevisionConflict, code: "ENVIRONMENT_REVISION_CONFLICT"},
		{err: errors.New("private /Users/person/state.sqlite"), code: "ACTIONS_UNAVAILABLE"},
	}
	for _, test := range tests {
		store := newFakeActionOperationStore()
		store.createErr = test.err
		coordinator := fakeActionCoordinator{
			start: func(context.Context, environmentcontrol.StartRequest) (environmentcontrol.EnvironmentResult, error) {
				return environmentcontrol.EnvironmentResult{}, nil
			},
			stop: func(context.Context, environmentcontrol.StopRequest) (environmentcontrol.EnvironmentResult, error) {
				return environmentcontrol.EnvironmentResult{}, nil
			},
		}
		service := newTestActionService(t, context.Background(), store, fakeActionJournal{}, coordinator)
		_, err := service.StartEnvironment(context.Background(), validActionStartRequest())
		var actionError *ActionError
		if !errors.As(err, &actionError) || actionError.Contract.Code != test.code {
			t.Fatalf("error=%v action=%+v want code=%s", err, actionError, test.code)
		}
	}
}

func newTestActionService(
	t *testing.T,
	lifecycle context.Context,
	store EnvironmentActionStore,
	journal EnvironmentActionJournal,
	coordinator EnvironmentActionCoordinator,
) *EnvironmentActionService {
	t.Helper()
	identifiers := []string{"operation_1", "run_2", "operation_3", "run_4"}
	index := 0
	service, err := NewEnvironmentActionService(EnvironmentActionServiceConfig{
		Lifecycle: lifecycle, Store: store, Journal: journal, Coordinator: coordinator,
		Resolver: fakeActionResolver{start: EnvironmentStartResolution{
			EnvironmentID: "environment_01",
			Ports: []portlease.Reservation{{
				Key:            portlease.Key{EnvironmentID: "environment_01", ServiceID: "storefront", Purpose: "http"},
				PreferredPorts: []int{7005},
			}},
			Intent: environmentcontrol.PlanIntent{ProfileDigest: "sample", ServiceIDs: []string{"storefront"}},
		}},
		NewID: func(string) (string, error) {
			identifier := identifiers[index]
			index++
			return identifier, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func validActionStartRequest() contractv2.StartEnvironmentRequest {
	return contractv2.StartEnvironmentRequest{
		MutationRequest: contractv2.MutationRequest{
			SchemaVersion: contractv2.SchemaVersion, RequestID: "request_01", IdempotencyKey: "idempotency_" + "01",
		},
		WorktreeID: "worktree_01", ServiceIDs: []string{"storefront"},
	}
}
