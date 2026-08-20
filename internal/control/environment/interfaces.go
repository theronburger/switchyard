package environment

import (
	"context"

	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

// OperationJournal is the persistence boundary for orchestration. Create and
// Update must be durable before they return. Publish atomically stores the final
// operation and environment result so clients never observe a half-built result.
type OperationJournal interface {
	Create(context.Context, OperationRecord) error
	Update(context.Context, OperationRecord) error
	Publish(context.Context, OperationRecord, EnvironmentResult) error
	Current(context.Context, string) (EnvironmentResult, bool, error)
	ListCurrent(context.Context, string, int) (CurrentEnvironmentPage, error)
	RefreshCurrent(context.Context, EnvironmentResult) (bool, error)
	Incomplete(context.Context) ([]OperationRecord, error)
}

type PortAllocator interface {
	ReserveSet(context.Context, []portlease.Reservation) ([]portlease.Lease, error)
	CheckBeforeLaunch(context.Context, portlease.Key) error
	Release(portlease.Key) bool
	Leases() []portlease.Lease
}

// PlanBuilder is deliberately context-free: Build is a deterministic, pure
// late-binding step over already assigned ports, not another mutation surface.
type PlanBuilder interface {
	Build(PlanningRequest) (ExecutionPlan, error)
}

type PreparationRunner interface {
	Run(context.Context, PreparationSpec) error
}

type ProjectionApplier interface {
	Plan(context.Context, string, string, ProjectionRequest, []portlease.Lease) (ProjectionChange, error)
	Apply(context.Context, ProjectionChange) error
	Rollback(context.Context, ProjectionChange) error
}

type InfrastructureHost interface {
	Ensure(context.Context, []containerhost.Goal) error
	StopOwned(context.Context, []containerhost.Goal) error
}

type ProcessHost interface {
	Start(context.Context, processhost.LaunchSpec) (processhost.Ownership, error)
	Stop(context.Context, string) (processhost.Observation, error)
	Reconcile(context.Context, string) (processhost.Observation, error)
}

type ReadinessTarget struct {
	EnvironmentID string
	RunID         string
	Service       ServiceResult
	Ports         []portlease.Lease
	Spec          ReadinessSpec
}

type ReadinessChecker interface {
	WaitReady(context.Context, ReadinessTarget) error
	CheckHealth(context.Context, ReadinessTarget) (HealthReport, error)
}

type ContainerPlanBuilder interface {
	Build(containerhost.Inventory, []containerhost.Goal) (containerhost.Plan, error)
}

type ContainerPlanApplier interface {
	Apply(context.Context, containerhost.Plan) error
}

// ContainerInfrastructureHost adapts the existing safe container inventory,
// planner, and reconciler to the coordinator's small ensure/stop interface.
type ContainerInfrastructureHost struct {
	Resources containerhost.ResourceReader
	Planner   ContainerPlanBuilder
	Applier   ContainerPlanApplier
}

func (host ContainerInfrastructureHost) Ensure(ctx context.Context, goals []containerhost.Goal) error {
	running := cloneGoals(goals)
	for index := range running {
		running[index].DesiredState = containerhost.DesiredRunning
	}
	return host.apply(ctx, running)
}

func (host ContainerInfrastructureHost) StopOwned(ctx context.Context, goals []containerhost.Goal) error {
	absent := cloneGoals(goals)
	for index := range absent {
		absent[index].DesiredState = containerhost.DesiredAbsent
		absent[index].Image = ""
		absent[index].PortBindings = nil
		absent[index].Environment = nil
	}
	return host.apply(ctx, absent)
}

func (host ContainerInfrastructureHost) apply(ctx context.Context, goals []containerhost.Goal) error {
	if host.Resources == nil || host.Planner == nil || host.Applier == nil {
		return ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	inventory, err := host.Resources.Inventory(ctx)
	if err != nil {
		return err
	}
	plan, err := host.Planner.Build(inventory, goals)
	if err != nil {
		return err
	}
	if len(plan.Protections) != 0 {
		return ErrProtectedInfra
	}
	if len(plan.Actions) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return host.Applier.Apply(ctx, plan)
}
