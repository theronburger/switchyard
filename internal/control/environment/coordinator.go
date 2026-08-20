package environment

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

const defaultRollbackTimeout = 15 * time.Second

type Config struct {
	Journal         OperationJournal
	Ports           PortAllocator
	Planner         PlanBuilder
	Preparations    PreparationRunner
	Projections     ProjectionApplier
	Infrastructure  InfrastructureHost
	Processes       ProcessHost
	Readiness       ReadinessChecker
	Now             func() time.Time
	RollbackTimeout time.Duration
}

type Coordinator struct {
	journal         OperationJournal
	ports           PortAllocator
	planner         PlanBuilder
	preparations    PreparationRunner
	projections     ProjectionApplier
	infrastructure  InfrastructureHost
	processes       ProcessHost
	readiness       ReadinessChecker
	now             func() time.Time
	rollbackTimeout time.Duration
	locksMutex      sync.Mutex
	environmentLock map[string]*sync.Mutex
}

func NewCoordinator(config Config) (*Coordinator, error) {
	if config.Journal == nil || config.Ports == nil {
		return nil, errors.New("environment coordinator journal and port allocator are required")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.RollbackTimeout <= 0 {
		config.RollbackTimeout = defaultRollbackTimeout
	}
	return &Coordinator{
		journal:         config.Journal,
		ports:           config.Ports,
		planner:         config.Planner,
		preparations:    config.Preparations,
		projections:     config.Projections,
		infrastructure:  config.Infrastructure,
		processes:       config.Processes,
		readiness:       config.Readiness,
		now:             config.Now,
		rollbackTimeout: config.RollbackTimeout,
		environmentLock: make(map[string]*sync.Mutex),
	}, nil
}

func (coordinator *Coordinator) Start(ctx context.Context, request StartRequest) (EnvironmentResult, error) {
	if err := validateStartRequest(request); err != nil {
		return EnvironmentResult{}, err
	}
	lock := coordinator.lockFor(request.EnvironmentID)
	lock.Lock()
	defer lock.Unlock()

	if err := ctx.Err(); err != nil {
		return EnvironmentResult{}, err
	}
	if err := coordinator.ensureNoIncomplete(ctx, request.EnvironmentID); err != nil {
		return EnvironmentResult{}, err
	}
	current, exists, err := coordinator.journal.Current(ctx, request.EnvironmentID)
	if err != nil {
		return EnvironmentResult{}, err
	}
	currentState := domain.EnvironmentUnknown
	if exists {
		currentState = current.State
	}
	if currentState != domain.EnvironmentUnknown && currentState != domain.EnvironmentStopped &&
		currentState != domain.EnvironmentFailed {
		return EnvironmentResult{}, fmt.Errorf("%w: cannot start from %s", ErrInvalidState, currentState)
	}
	if exists && currentState == domain.EnvironmentFailed && hasResources(current) {
		return EnvironmentResult{}, fmt.Errorf("%w: failed environment must be stopped before restart", ErrInvalidState)
	}
	if request.Intent != nil && coordinator.planner == nil {
		return EnvironmentResult{}, ErrInvalidRequest
	}

	operation := OperationRecord{
		ID: request.OperationID, EnvironmentID: request.EnvironmentID, Kind: OperationStart,
		RunID: request.RunID,
		State: domain.OperationPending, EnvironmentState: currentState, Phase: PhasePending,
		Intent: cloneIntent(request.Intent),
		Source: cloneSource(request.Source),
	}
	if err := coordinator.journal.Create(ctx, operation); err != nil {
		return EnvironmentResult{}, err
	}
	if err := transitionEnvironment(&operation, domain.EnvironmentStarting); err != nil {
		return EnvironmentResult{}, err
	}
	if err := transitionOperation(&operation, domain.OperationRunning); err != nil {
		return EnvironmentResult{}, err
	}
	if err := coordinator.journal.Update(ctx, operation); err != nil {
		return EnvironmentResult{}, err
	}

	targetID := ""
	if request.Intent != nil {
		targetID = request.Intent.TargetID
	}
	result := EnvironmentResult{
		EnvironmentID: request.EnvironmentID,
		RunID:         request.RunID,
		TargetID:      targetID,
		State:         domain.EnvironmentStarting,
		Source:        cloneSource(request.Source),
		UpdatedAt:     coordinator.now().UTC(),
	}

	if len(request.Ports) != 0 {
		newPortKeys := unleasedReservationKeys(request.Ports, coordinator.ports.Leases())
		entryIndex := -1
		if len(newPortKeys) != 0 {
			entry := RollbackEntry{Kind: RollbackPorts, Armed: true, PortKeys: newPortKeys}
			entryIndex, err = coordinator.arm(ctx, &operation, PhaseReservingPorts, entry)
			if err != nil {
				return coordinator.failStart(operation, result, err)
			}
		} else if err := coordinator.checkpoint(ctx, &operation, PhaseReservingPorts); err != nil {
			return coordinator.failStart(operation, result, err)
		}
		leases, err := coordinator.ports.ReserveSet(ctx, request.Ports)
		if err != nil {
			return coordinator.failStart(operation, result, err)
		}
		result.Ports = cloneLeases(leases)
		if entryIndex >= 0 {
			operation.Rollback[entryIndex].Leases = leasesForKeys(leases, newPortKeys)
			if err := coordinator.applied(ctx, &operation, entryIndex); err != nil {
				return coordinator.failStart(operation, result, err)
			}
		}
	}

	plan := ExecutionPlan{}
	if request.Intent != nil {
		plan, err = coordinator.planner.Build(PlanningRequest{
			EnvironmentID: request.EnvironmentID,
			RunID:         request.RunID,
			Intent:        *cloneIntent(request.Intent),
			AssignedPorts: cloneLeases(result.Ports),
		})
		if err != nil {
			return coordinator.failStart(operation, result, err)
		}
		if err := validateExecutionPlan(request.EnvironmentID, request.RunID, result.Ports, plan); err != nil {
			return coordinator.failStart(operation, result, err)
		}
	}
	if err := coordinator.requireExecutionDependencies(plan); err != nil {
		return coordinator.failStart(operation, result, err)
	}

	for _, preparation := range plan.Preparations {
		if err := coordinator.checkpoint(ctx, &operation, PhasePreparingServices); err != nil {
			return coordinator.failStart(operation, result, err)
		}
		if err := coordinator.preparations.Run(ctx, clonePreparation(preparation)); err != nil {
			return coordinator.failStart(operation, result, err)
		}
	}

	if plan.Projection != nil {
		change, err := coordinator.projections.Plan(
			ctx, request.EnvironmentID, request.RunID, *plan.Projection, cloneLeases(result.Ports),
		)
		if err != nil {
			return coordinator.failStart(operation, result, err)
		}
		if err := validateProjectionChange(request.EnvironmentID, request.RunID, change); err != nil {
			return coordinator.failStart(operation, result, err)
		}
		entry := RollbackEntry{Kind: RollbackProjection, Armed: true, Projection: cloneProjection(&change)}
		entryIndex, err := coordinator.arm(ctx, &operation, PhaseMaterializing, entry)
		if err != nil {
			return coordinator.failStart(operation, result, err)
		}
		if err := coordinator.projections.Apply(ctx, change); err != nil {
			return coordinator.failStart(operation, result, err)
		}
		result.Projection = cloneProjection(&change)
		if err := coordinator.applied(ctx, &operation, entryIndex); err != nil {
			return coordinator.failStart(operation, result, err)
		}
	}

	if len(plan.Infrastructure) != 0 {
		goals := cloneGoals(plan.Infrastructure)
		entry := RollbackEntry{Kind: RollbackInfrastructure, Armed: true, Infrastructure: goals}
		entryIndex, err := coordinator.arm(ctx, &operation, PhaseEnsuringInfrastructure, entry)
		if err != nil {
			return coordinator.failStart(operation, result, err)
		}
		if err := coordinator.infrastructure.Ensure(ctx, cloneGoals(goals)); err != nil {
			return coordinator.failStart(operation, result, err)
		}
		result.Infrastructure = cloneGoals(goals)
		if err := coordinator.applied(ctx, &operation, entryIndex); err != nil {
			return coordinator.failStart(operation, result, err)
		}
	}

	for _, initialization := range plan.Initializations {
		if err := coordinator.checkpoint(ctx, &operation, PhaseInitializingInfrastructure); err != nil {
			return coordinator.failStart(operation, result, err)
		}
		if err := coordinator.preparations.Run(ctx, clonePreparation(initialization)); err != nil {
			return coordinator.failStart(operation, result, err)
		}
	}

	serviceStages := plan.ServiceStages
	if len(serviceStages) == 0 && len(plan.Services) != 0 {
		serviceStages = [][]ServiceLaunch{plan.Services}
	}
	for _, stage := range serviceStages {
		stageStart := len(result.Services)
		for _, service := range stage {
			if err := coordinator.checkPortsBeforeLaunch(ctx, service.PortKeys); err != nil {
				return coordinator.failStart(operation, result, err)
			}
			serviceResult := ServiceResult{
				ID: service.ID, EnvironmentID: request.EnvironmentID, RunID: request.RunID,
				OwnershipPath: filepath.Join(service.Process.RunDirectory, processhost.OwnershipFileName), Owned: true,
				Readiness: service.Readiness,
			}
			entry := RollbackEntry{Kind: RollbackProcess, Armed: true, Process: cloneService(&serviceResult)}
			entryIndex, err := coordinator.arm(ctx, &operation, PhaseLaunchingServices, entry)
			if err != nil {
				return coordinator.failStart(operation, result, err)
			}
			ownership, err := coordinator.processes.Start(ctx, service.Process)
			if err != nil {
				return coordinator.failStart(operation, result, err)
			}
			if ownership.EnvironmentID != request.EnvironmentID || ownership.ServiceID != service.ID ||
				ownership.RunID != request.RunID {
				operation.Rollback[entryIndex].Armed = false
				_ = coordinator.journal.Update(ctx, operation)
				return coordinator.failStart(operation, result, ErrForeignOwnership)
			}
			serviceResult.Process = ownership
			serviceResult.Observation = ServiceObservation{
				State: "running", ProcessCount: len(ownership.Members), ObservedAt: coordinator.now().UTC(),
			}
			result.Services = append(result.Services, serviceResult)
			operation.Rollback[entryIndex].Process = cloneService(&serviceResult)
			if err := coordinator.applied(ctx, &operation, entryIndex); err != nil {
				return coordinator.failStart(operation, result, err)
			}
		}
		if err := coordinator.checkpoint(ctx, &operation, PhaseWaitingReadiness); err != nil {
			return coordinator.failStart(operation, result, err)
		}
		for index, service := range stage {
			err := coordinator.readiness.WaitReady(ctx, ReadinessTarget{
				EnvironmentID: request.EnvironmentID, RunID: request.RunID,
				Service: result.Services[stageStart+index], Ports: cloneLeases(result.Ports), Spec: service.Readiness,
			})
			if err != nil {
				return coordinator.failStart(operation, result, err)
			}
		}
	}

	services := flattenedServiceStages(plan)
	if len(services) != 0 {
		for index, service := range services {
			health, err := coordinator.readiness.CheckHealth(ctx, ReadinessTarget{
				EnvironmentID: request.EnvironmentID, RunID: request.RunID,
				Service: result.Services[index], Ports: cloneLeases(result.Ports), Spec: service.Readiness,
			})
			if err != nil {
				return coordinator.failStart(operation, result, err)
			}
			result.Services[index].Health = health
		}
		if err := coordinator.verifyStartedProcesses(ctx, &result); err != nil {
			return coordinator.failStart(operation, result, err)
		}
	}

	if err := transitionEnvironment(&operation, domain.EnvironmentRunning); err != nil {
		return coordinator.failStart(operation, result, err)
	}
	if err := transitionOperation(&operation, domain.OperationSucceeded); err != nil {
		return coordinator.failStart(operation, result, err)
	}
	operation.Phase = PhaseComplete
	result.State = domain.EnvironmentRunning
	result.UpdatedAt = coordinator.now().UTC()
	if err := coordinator.journal.Publish(ctx, operation, cloneEnvironment(result)); err != nil {
		return coordinator.failStart(operation, result, err)
	}
	return result, nil
}

func (coordinator *Coordinator) verifyStartedProcesses(
	ctx context.Context,
	result *EnvironmentResult,
) error {
	for index := range result.Services {
		observation, err := coordinator.processes.Reconcile(ctx, result.Services[index].OwnershipPath)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrForeignOwnership
		}
		if !observation.OwnershipVerified {
			return ErrForeignOwnership
		}
		if observation.State != "running" || observation.MemberCount <= 0 {
			return ErrProcessNotRunning
		}
		result.Services[index].Observation = serviceObservation(observation, nil, coordinator.now().UTC())
	}
	return nil
}

func (coordinator *Coordinator) Stop(ctx context.Context, request StopRequest) (EnvironmentResult, error) {
	if request.OperationID == "" || request.EnvironmentID == "" {
		return EnvironmentResult{}, ErrInvalidRequest
	}
	lock := coordinator.lockFor(request.EnvironmentID)
	lock.Lock()
	defer lock.Unlock()

	if err := ctx.Err(); err != nil {
		return EnvironmentResult{}, err
	}
	if err := coordinator.ensureNoIncomplete(ctx, request.EnvironmentID); err != nil {
		return EnvironmentResult{}, err
	}
	current, exists, err := coordinator.journal.Current(ctx, request.EnvironmentID)
	if err != nil {
		return EnvironmentResult{}, err
	}
	if !exists {
		return EnvironmentResult{}, fmt.Errorf("%w: environment does not exist", ErrInvalidState)
	}
	if current.State != domain.EnvironmentRunning && current.State != domain.EnvironmentFailed &&
		current.State != domain.EnvironmentStopped {
		return EnvironmentResult{}, fmt.Errorf("%w: cannot stop from %s", ErrInvalidState, current.State)
	}
	if err := coordinator.validateOwnedResult(current); err != nil {
		return EnvironmentResult{}, err
	}
	if err := coordinator.requireStopDependencies(current); err != nil {
		return EnvironmentResult{}, err
	}

	operation := OperationRecord{
		ID: request.OperationID, EnvironmentID: request.EnvironmentID, Kind: OperationStop,
		RunID: current.RunID,
		State: domain.OperationPending, EnvironmentState: current.State, Phase: PhasePending,
		Target: environmentPointer(current),
	}
	if err := coordinator.journal.Create(ctx, operation); err != nil {
		return EnvironmentResult{}, err
	}
	if current.State != domain.EnvironmentStopped {
		if err := transitionEnvironment(&operation, domain.EnvironmentStopping); err != nil {
			return EnvironmentResult{}, err
		}
	}
	if err := transitionOperation(&operation, domain.OperationRunning); err != nil {
		return EnvironmentResult{}, err
	}
	if err := coordinator.journal.Update(ctx, operation); err != nil {
		return EnvironmentResult{}, err
	}

	if err := coordinator.stopTarget(ctx, &operation, current); err != nil {
		return coordinator.failStop(operation, current, err)
	}
	return coordinator.publishStopped(ctx, operation, current)
}

func (coordinator *Coordinator) checkPortsBeforeLaunch(ctx context.Context, keys []portlease.Key) error {
	for _, key := range keys {
		if err := coordinator.ports.CheckBeforeLaunch(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func (coordinator *Coordinator) arm(
	ctx context.Context,
	operation *OperationRecord,
	phase OperationPhase,
	entry RollbackEntry,
) (int, error) {
	operation.Phase = phase
	operation.Rollback = append(operation.Rollback, entry)
	index := len(operation.Rollback) - 1
	if err := coordinator.journal.Update(ctx, *operation); err != nil {
		return index, err
	}
	return index, nil
}

func (coordinator *Coordinator) applied(ctx context.Context, operation *OperationRecord, index int) error {
	operation.Rollback[index].Applied = true
	return coordinator.journal.Update(ctx, *operation)
}

func (coordinator *Coordinator) checkpoint(ctx context.Context, operation *OperationRecord, phase OperationPhase) error {
	operation.Phase = phase
	return coordinator.journal.Update(ctx, *operation)
}

func (coordinator *Coordinator) ensureNoIncomplete(ctx context.Context, environmentID string) error {
	operations, err := coordinator.journal.Incomplete(ctx)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if operation.EnvironmentID == environmentID {
			return fmt.Errorf("%w: environment has incomplete operation %s", ErrInvalidState, operation.ID)
		}
	}
	return nil
}

func (coordinator *Coordinator) lockFor(environmentID string) *sync.Mutex {
	coordinator.locksMutex.Lock()
	defer coordinator.locksMutex.Unlock()
	if lock, exists := coordinator.environmentLock[environmentID]; exists {
		return lock
	}
	lock := &sync.Mutex{}
	coordinator.environmentLock[environmentID] = lock
	return lock
}
