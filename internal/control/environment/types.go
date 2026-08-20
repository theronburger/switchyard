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
	ErrInvalidRequest    = errors.New("environment operation request is invalid")
	ErrInvalidState      = errors.New("environment state does not allow this operation")
	ErrForeignOwnership  = errors.New("environment contains a foreign or unverifiable resource")
	ErrProtectedInfra    = errors.New("container infrastructure is protected from mutation")
	ErrProcessNotRunning = errors.New("environment process is not running after readiness")
)

type OperationKind string

const (
	OperationStart OperationKind = "environment.start"
	OperationStop  OperationKind = "environment.stop"
)

type OperationPhase string

const (
	PhasePending                    OperationPhase = "pending"
	PhaseReservingPorts             OperationPhase = "reserving-ports"
	PhasePreparingServices          OperationPhase = "preparing-services"
	PhaseMaterializing              OperationPhase = "materializing-projection"
	PhaseEnsuringInfrastructure     OperationPhase = "ensuring-infrastructure"
	PhaseInitializingInfrastructure OperationPhase = "initializing-infrastructure"
	PhaseLaunchingServices          OperationPhase = "launching-services"
	PhaseWaitingReadiness           OperationPhase = "waiting-readiness"
	PhaseStoppingServices           OperationPhase = "stopping-services"
	PhaseStoppingInfrastructure     OperationPhase = "stopping-infrastructure"
	PhaseRemovingProjection         OperationPhase = "removing-projection"
	PhaseReleasingPorts             OperationPhase = "releasing-ports"
	PhaseRollingBack                OperationPhase = "rolling-back"
	PhasePublishingResult           OperationPhase = "publishing-result"
	PhaseComplete                   OperationPhase = "complete"
)

type RollbackKind string

const (
	RollbackPorts          RollbackKind = "release-ports"
	RollbackProjection     RollbackKind = "rollback-projection"
	RollbackInfrastructure RollbackKind = "stop-infrastructure"
	RollbackProcess        RollbackKind = "stop-process"
)

type ProjectionRequest struct {
	ID          string
	ArtifactIDs []string
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

const (
	ServiceObservationProcessFailed       = "PROCESS_OBSERVATION_FAILED"
	ServiceObservationOwnershipUnverified = "PROCESS_OWNERSHIP_UNVERIFIED"
	ServiceObservationHealthFailed        = "HEALTH_OBSERVATION_FAILED"
	OperationFailureOwnershipUnverified   = "process ownership could not be verified"
)

type ServiceObservation struct {
	State        string
	ProcessCount int
	MemoryBytes  int64
	CPUPercent   float64
	Code         string
	ObservedAt   time.Time
}

type PreparationSpec struct {
	ID           string
	ServiceID    string
	LogReference string
	Executable   string
	Arguments    []string
	Environment  []string
	Directory    string
	RunDirectory string
	Timeout      time.Duration
}

type OperationFailure struct {
	Code         string
	Message      string
	Retryable    bool
	ResourceKind string
	ResourceID   string
	Phase        OperationPhase
	Step         string
	Diagnostic   string
	LogReference string
	NextAction   string
	ExitCode     *int
}

type OperationFailureProvider interface {
	OperationFailure() OperationFailure
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
	TargetID   string
	ServiceIDs []string
}

// SourceSnapshot is the repository state captured immediately before an
// environment run is accepted. It is immutable provenance for that run, not a
// live repository observation.
type SourceSnapshot struct {
	Revision          string
	HasTrackedChanges bool
	HasUntrackedFiles bool
	ObservedAt        time.Time
}

type PlanningRequest struct {
	EnvironmentID string
	RunID         string
	Intent        PlanIntent
	AssignedPorts []portlease.Lease
}

type ExecutionPlan struct {
	Preparations    []PreparationSpec
	Projection      *ProjectionRequest
	Infrastructure  []containerhost.Goal
	Initializations []PreparationSpec
	Services        []ServiceLaunch
}

type ServiceResult struct {
	ID            string
	EnvironmentID string
	RunID         string
	OwnershipPath string
	Owned         bool
	Process       processhost.Ownership
	Readiness     ReadinessSpec
	Health        HealthReport
	Observation   ServiceObservation
}

type StartRequest struct {
	OperationID   string
	EnvironmentID string
	RunID         string
	Ports         []portlease.Reservation
	Intent        *PlanIntent
	Source        *SourceSnapshot
}

type StopRequest struct {
	OperationID   string
	EnvironmentID string
}

type EnvironmentResult struct {
	EnvironmentID  string
	RunID          string
	TargetID       string
	State          domain.EnvironmentState
	Ports          []portlease.Lease
	Projection     *ProjectionChange
	Infrastructure []containerhost.Goal
	Services       []ServiceResult
	Source         *SourceSnapshot
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
	Source           *SourceSnapshot
	Target           *EnvironmentResult
	Failure          string
	FailureDetail    *OperationFailure
}

type ReconcileOutcome struct {
	OperationID   string
	EnvironmentID string
	State         domain.EnvironmentState
	Err           error
}

type CurrentEnvironmentPage struct {
	Results           []EnvironmentResult
	NextEnvironmentID string
	HasMore           bool
}
