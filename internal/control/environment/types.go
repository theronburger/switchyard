package environment

import (
	"errors"
	"time"

	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

var (
	ErrInvalidRequest   = errors.New("environment operation request is invalid")
	ErrInvalidState     = errors.New("environment state does not allow this operation")
	ErrForeignOwnership = errors.New("environment contains a foreign or unverifiable resource")
	ErrProtectedInfra   = errors.New("container infrastructure is protected from mutation")
)

type OperationKind string

const (
	OperationStart OperationKind = "environment.start"
	OperationStop  OperationKind = "environment.stop"
)

type OperationPhase string

const (
	PhasePending                OperationPhase = "pending"
	PhaseReservingPorts         OperationPhase = "reserving-ports"
	PhasePreparingServices      OperationPhase = "preparing-services"
	PhaseMaterializing          OperationPhase = "materializing-projection"
	PhaseEnsuringInfrastructure OperationPhase = "ensuring-infrastructure"
	PhaseLaunchingServices      OperationPhase = "launching-services"
	PhaseWaitingReadiness       OperationPhase = "waiting-readiness"
	PhaseStoppingServices       OperationPhase = "stopping-services"
	PhaseStoppingInfrastructure OperationPhase = "stopping-infrastructure"
	PhaseRemovingProjection     OperationPhase = "removing-projection"
	PhaseReleasingPorts         OperationPhase = "releasing-ports"
	PhaseRollingBack            OperationPhase = "rolling-back"
	PhaseComplete               OperationPhase = "complete"
)

type RollbackKind string

const (
	RollbackPorts          RollbackKind = "release-ports"
	RollbackProjection     RollbackKind = "rollback-projection"
	RollbackInfrastructure RollbackKind = "stop-infrastructure"
	RollbackProcess        RollbackKind = "stop-process"
)

type ProjectionRequest struct {
	ID string
}

type ProjectionChange struct {
	ID            string
	EnvironmentID string
	RunID         string
	RollbackToken string
	Owned         bool
}

type ReadinessSpec struct {
	ID string
}

type HealthReport struct {
	Readiness string
	Health    string
}

type PreparationSpec struct {
	ID           string
	Executable   string
	Arguments    []string
	Environment  []string
	Directory    string
	RunDirectory string
	Timeout      time.Duration
}

type ServiceLaunch struct {
	ID        string
	Process   processhost.LaunchSpec
	PortKeys  []portlease.Key
	Readiness ReadinessSpec
}

// PlanIntent is the small, persistence-safe input to deterministic late
// binding. Adapter-specific planners retain repository paths and other local
// details; the coordinator persists only this intent and assigned leases.
type PlanIntent struct {
	Adapter    string
	ServiceIDs []string
}

type PlanningRequest struct {
	EnvironmentID string
	RunID         string
	Intent        PlanIntent
	AssignedPorts []portlease.Lease
}

type ExecutionPlan struct {
	Preparations   []PreparationSpec
	Projection     *ProjectionRequest
	Infrastructure []containerhost.Goal
	Services       []ServiceLaunch
}

type ServiceResult struct {
	ID            string
	EnvironmentID string
	RunID         string
	OwnershipPath string
	Owned         bool
	Process       processhost.Ownership
	Health        HealthReport
}

type StartRequest struct {
	OperationID   string
	EnvironmentID string
	RunID         string
	Ports         []portlease.Reservation
	Intent        *PlanIntent
}

type StopRequest struct {
	OperationID   string
	EnvironmentID string
}

type EnvironmentResult struct {
	EnvironmentID  string
	RunID          string
	State          domain.EnvironmentState
	Ports          []portlease.Lease
	Projection     *ProjectionChange
	Infrastructure []containerhost.Goal
	Services       []ServiceResult
	UpdatedAt      time.Time
}

type RollbackEntry struct {
	Kind           RollbackKind
	Armed          bool
	Applied        bool
	PortKeys       []portlease.Key
	Leases         []portlease.Lease
	Projection     *ProjectionChange
	Infrastructure []containerhost.Goal
	Process        *ServiceResult
}

type OperationRecord struct {
	ID               string
	EnvironmentID    string
	RunID            string
	Kind             OperationKind
	State            domain.OperationState
	EnvironmentState domain.EnvironmentState
	Phase            OperationPhase
	Rollback         []RollbackEntry
	Intent           *PlanIntent
	Target           *EnvironmentResult
	Failure          string
}

type ReconcileOutcome struct {
	OperationID   string
	EnvironmentID string
	State         domain.EnvironmentState
	Err           error
}
