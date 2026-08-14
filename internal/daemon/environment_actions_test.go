package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/state"
)

type fakeActionOperationStore struct {
	mutex       sync.Mutex
	operations  map[string]contractv1.Operation
	byKey       map[string]state.NewOperation
	createErr   error
	transitions int
}

func newFakeActionOperationStore() *fakeActionOperationStore {
	return &fakeActionOperationStore{
		operations: make(map[string]contractv1.Operation),
		byKey:      make(map[string]state.NewOperation),
	}
}

func (store *fakeActionOperationStore) CreateOperation(
	_ context.Context,
	request state.NewOperation,
) (contractv1.Operation, bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	if store.createErr != nil {
		return contractv1.Operation{}, false, store.createErr
	}
	if existing, found := store.byKey[request.IdempotencyKey]; found {
		if existing.RequestFingerprint != request.RequestFingerprint {
			return contractv1.Operation{}, false, state.ErrIdempotencyConflict
		}
		return store.operations[existing.ID], false, nil
	}
	now := time.Date(2026, 8, 14, 16, 30, 0, 0, time.UTC)
	operation := contractv1.Operation{
		ID: request.ID, Kind: request.Kind, State: string(domain.OperationPending),
		EnvironmentID: request.EnvironmentID, CreatedAt: now, UpdatedAt: now,
	}
	store.byKey[request.IdempotencyKey] = request
	store.operations[request.ID] = operation
	return operation, true, nil
}

func (store *fakeActionOperationStore) ReadOperation(
	_ context.Context,
	operationID string,
) (contractv1.Operation, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	operation, found := store.operations[operationID]
	if !found {
		return contractv1.Operation{}, state.ErrOperationNotFound
	}
	return operation, nil
}

func (store *fakeActionOperationStore) TransitionOperation(
	_ context.Context,
	operationID string,
	nextState string,
	failure *contractv1.ContractError,
) (contractv1.Operation, error) {
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

func (resolver fakeActionResolver) ResolveStart(
	context.Context,
	contractv1.StartEnvironmentRequest,
) (EnvironmentStartResolution, error) {
	return resolver.start, resolver.err
}

func (resolver fakeActionResolver) ResolveStop(
	_ context.Context,
	environmentID string,
	_ contractv1.StopEnvironmentRequest,
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
	if retried.OperationID != receipt.OperationID || receipt.Validate() != nil {
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
				Key:            portlease.Key{EnvironmentID: "environment_01", ServiceID: "organizer", Purpose: "http"},
				PreferredPorts: []int{7005},
			}},
			Intent: environmentcontrol.PlanIntent{Adapter: "marketplace", ServiceIDs: []string{"organizer"}},
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

func validActionStartRequest() contractv1.StartEnvironmentRequest {
	return contractv1.StartEnvironmentRequest{
		MutationRequest: contractv1.MutationRequest{
			SchemaVersion: contractv1.SchemaVersion, RequestID: "request_01", IdempotencyKey: "idempotency_" + "01",
		},
		WorktreeID: "worktree_01", ServiceIDs: []string{"organizer"},
	}
}
