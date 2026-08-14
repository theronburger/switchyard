package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"sync"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/state"
)

const actionFinalizationTimeout = 5 * time.Second

type EnvironmentActionStore interface {
	CreateOperation(context.Context, state.NewOperation) (contractv1.Operation, bool, error)
	ReadOperation(context.Context, string) (contractv1.Operation, error)
	TransitionOperation(context.Context, string, string, *contractv1.ContractError) (contractv1.Operation, error)
}

type EnvironmentActionJournal interface {
	Incomplete(context.Context) ([]environmentcontrol.OperationRecord, error)
}

type EnvironmentActionCoordinator interface {
	Start(context.Context, environmentcontrol.StartRequest) (environmentcontrol.EnvironmentResult, error)
	Stop(context.Context, environmentcontrol.StopRequest) (environmentcontrol.EnvironmentResult, error)
}

type EnvironmentStartResolution struct {
	EnvironmentID string
	Ports         []portlease.Reservation
	Intent        environmentcontrol.PlanIntent
}

type EnvironmentActionResolver interface {
	ResolveStart(context.Context, contractv1.StartEnvironmentRequest) (EnvironmentStartResolution, error)
	ResolveStop(context.Context, string, contractv1.StopEnvironmentRequest) error
}

type EnvironmentActionServiceConfig struct {
	Lifecycle   context.Context
	Store       EnvironmentActionStore
	Journal     EnvironmentActionJournal
	Coordinator EnvironmentActionCoordinator
	Resolver    EnvironmentActionResolver
	NewID       func(string) (string, error)
}

type EnvironmentActionService struct {
	store       EnvironmentActionStore
	journal     EnvironmentActionJournal
	coordinator EnvironmentActionCoordinator
	resolver    EnvironmentActionResolver
	newID       func(string) (string, error)
	lifecycle   context.Context
	cancel      context.CancelFunc
	mutex       sync.Mutex
	closed      bool
	workers     sync.WaitGroup
}

func NewEnvironmentActionService(config EnvironmentActionServiceConfig) (*EnvironmentActionService, error) {
	if config.Lifecycle == nil || config.Store == nil || config.Journal == nil ||
		config.Coordinator == nil || config.Resolver == nil {
		return nil, errors.New("environment action service dependencies are required")
	}
	if config.NewID == nil {
		config.NewID = randomActionID
	}
	lifecycle, cancel := context.WithCancel(config.Lifecycle)
	return &EnvironmentActionService{
		store: config.Store, journal: config.Journal, coordinator: config.Coordinator,
		resolver: config.Resolver, newID: config.NewID, lifecycle: lifecycle, cancel: cancel,
	}, nil
}

func (service *EnvironmentActionService) StartEnvironment(
	ctx context.Context,
	request contractv1.StartEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	if err := request.Validate(); err != nil {
		return contractv1.MutationReceipt{}, invalidActionRequest()
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed || service.lifecycle.Err() != nil {
		return contractv1.MutationReceipt{}, actionsUnavailable()
	}
	resolution, err := service.resolver.ResolveStart(ctx, request)
	if err != nil {
		return contractv1.MutationReceipt{}, safeResolutionError(err)
	}
	if resolution.EnvironmentID == "" || resolution.Intent.Adapter == "" ||
		len(resolution.Intent.ServiceIDs) == 0 {
		return contractv1.MutationReceipt{}, invalidActionRequest()
	}
	operationID, err := service.newID("operation")
	if err != nil {
		return contractv1.MutationReceipt{}, actionsUnavailable()
	}
	runID, err := service.newID("run")
	if err != nil {
		return contractv1.MutationReceipt{}, actionsUnavailable()
	}
	fingerprint, err := state.FingerprintRequest(request)
	if err != nil {
		return contractv1.MutationReceipt{}, invalidActionRequest()
	}
	operation, created, err := service.store.CreateOperation(ctx, state.NewOperation{
		ID: operationID, RequestID: request.RequestID, IdempotencyKey: request.IdempotencyKey,
		RequestFingerprint: fingerprint, Kind: string(environmentcontrol.OperationStart),
		EnvironmentID:               resolution.EnvironmentID,
		ExpectedEnvironmentRevision: request.ExpectedEnvironmentRevision,
	})
	if err != nil {
		return contractv1.MutationReceipt{}, operationCreationError(err)
	}
	if created {
		service.workers.Add(1)
		go service.executeStart(environmentcontrol.StartRequest{
			OperationID: operation.ID, EnvironmentID: resolution.EnvironmentID, RunID: runID,
			Ports: cloneReservations(resolution.Ports), Intent: clonePlanIntent(resolution.Intent),
		})
	}
	return receiptForOperation(request.RequestID, operation), nil
}

func (service *EnvironmentActionService) StopEnvironment(
	ctx context.Context,
	environmentID string,
	request contractv1.StopEnvironmentRequest,
) (contractv1.MutationReceipt, error) {
	if environmentID == "" || request.Validate() != nil {
		return contractv1.MutationReceipt{}, invalidActionRequest()
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.closed || service.lifecycle.Err() != nil {
		return contractv1.MutationReceipt{}, actionsUnavailable()
	}
	if err := service.resolver.ResolveStop(ctx, environmentID, request); err != nil {
		return contractv1.MutationReceipt{}, safeResolutionError(err)
	}
	operationID, err := service.newID("operation")
	if err != nil {
		return contractv1.MutationReceipt{}, actionsUnavailable()
	}
	fingerprint, err := state.FingerprintRequest(struct {
		EnvironmentID string                            `json:"environmentId"`
		Request       contractv1.StopEnvironmentRequest `json:"request"`
	}{EnvironmentID: environmentID, Request: request})
	if err != nil {
		return contractv1.MutationReceipt{}, invalidActionRequest()
	}
	operation, created, err := service.store.CreateOperation(ctx, state.NewOperation{
		ID: operationID, RequestID: request.RequestID, IdempotencyKey: request.IdempotencyKey,
		RequestFingerprint: fingerprint, Kind: string(environmentcontrol.OperationStop),
		EnvironmentID:               environmentID,
		ExpectedEnvironmentRevision: request.ExpectedEnvironmentRevision,
	})
	if err != nil {
		return contractv1.MutationReceipt{}, operationCreationError(err)
	}
	if created {
		service.workers.Add(1)
		go service.executeStop(environmentcontrol.StopRequest{
			OperationID: operation.ID, EnvironmentID: environmentID,
		})
	}
	return receiptForOperation(request.RequestID, operation), nil
}

func (service *EnvironmentActionService) CloseAndWait(ctx context.Context) error {
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

func (service *EnvironmentActionService) executeStart(request environmentcontrol.StartRequest) {
	defer service.workers.Done()
	_, err := service.coordinator.Start(service.lifecycle, request)
	service.finalizeUnhandled(request.OperationID, err)
}

func (service *EnvironmentActionService) executeStop(request environmentcontrol.StopRequest) {
	defer service.workers.Done()
	_, err := service.coordinator.Stop(service.lifecycle, request)
	service.finalizeUnhandled(request.OperationID, err)
}

func (service *EnvironmentActionService) finalizeUnhandled(operationID string, actionErr error) {
	if actionErr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionFinalizationTimeout)
	defer cancel()
	incomplete, err := service.journal.Incomplete(ctx)
	if err != nil {
		return
	}
	for _, record := range incomplete {
		if record.ID == operationID {
			return
		}
	}
	operation, err := service.store.ReadOperation(ctx, operationID)
	if err != nil || (operation.State != string(domain.OperationPending) &&
		operation.State != string(domain.OperationRunning)) {
		return
	}
	failure := contractv1.ContractError{
		Code: "ENVIRONMENT_ACTION_FAILED", Message: "The environment action could not be completed.", Retryable: true,
	}
	_, _ = service.store.TransitionOperation(ctx, operationID, string(domain.OperationFailed), &failure)
}

func receiptForOperation(requestID string, operation contractv1.Operation) contractv1.MutationReceipt {
	return contractv1.MutationReceipt{
		SchemaVersion: contractv1.SchemaVersion, RequestID: requestID,
		OperationID: operation.ID, AcceptedAt: operation.CreatedAt, EnvironmentID: operation.EnvironmentID,
	}
}

func randomActionID(prefix string) (string, error) {
	contents := make([]byte, 16)
	if _, err := rand.Read(contents); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(contents), nil
}

func cloneReservations(source []portlease.Reservation) []portlease.Reservation {
	cloned := make([]portlease.Reservation, len(source))
	for index, reservation := range source {
		cloned[index] = reservation
		cloned[index].PreferredPorts = append([]int(nil), reservation.PreferredPorts...)
	}
	return cloned
}

func clonePlanIntent(intent environmentcontrol.PlanIntent) *environmentcontrol.PlanIntent {
	return &environmentcontrol.PlanIntent{
		Adapter: intent.Adapter, ServiceIDs: append([]string(nil), intent.ServiceIDs...),
	}
}

func operationCreationError(err error) error {
	switch {
	case errors.Is(err, state.ErrIdempotencyConflict):
		return &ActionError{Status: http.StatusConflict, Contract: contractv1.ContractError{
			Code: "IDEMPOTENCY_CONFLICT", Message: "The idempotency key was already used for another action.",
		}}
	case errors.Is(err, state.ErrEnvironmentRevisionConflict):
		return &ActionError{Status: http.StatusConflict, Contract: contractv1.ContractError{
			Code: "ENVIRONMENT_REVISION_CONFLICT", Message: "The environment changed before the action was accepted.", Retryable: true,
		}}
	default:
		return actionsUnavailable()
	}
}

func safeResolutionError(err error) error {
	var actionError *ActionError
	if errors.As(err, &actionError) && validActionError(actionError) {
		return actionError
	}
	return invalidActionRequest()
}

func invalidActionRequest() error {
	return &ActionError{Status: http.StatusBadRequest, Contract: contractv1.ContractError{
		Code: "INVALID_ENVIRONMENT_ACTION", Message: "The environment action is invalid.",
	}}
}

func actionsUnavailable() error {
	return &ActionError{Status: http.StatusServiceUnavailable, Contract: contractv1.ContractError{
		Code: "ACTIONS_UNAVAILABLE", Message: "Environment actions are temporarily unavailable.", Retryable: true,
	}}
}
