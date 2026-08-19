package workspace

import (
	"errors"
	"time"
)

var (
	ErrInvalidRequest = errors.New("workspace ensure request is invalid")
	ErrInvalidPlan    = errors.New("workspace plan is invalid")
	ErrInvalidRecord  = errors.New("workspace operation record is invalid")
	ErrStepFailed     = errors.New("workspace preparation step failed")
	ErrNotReady       = errors.New("workspace requirements are not ready")
)

type Ownership string

const (
	OwnershipAdopted Ownership = "adopted"
	OwnershipManaged Ownership = "managed"
)

type State string

const (
	StatePending State = "pending"
	StateRunning State = "running"
	StateReady   State = "ready"
	StateFailed  State = "failed"
)

type Phase string

const (
	PhasePending   Phase = "pending"
	PhasePreparing Phase = "preparing"
	PhaseVerifying Phase = "verifying"
	PhaseComplete  Phase = "complete"
)

type RequirementKind string

const (
	RequirementDirectory   RequirementKind = "directory"
	RequirementRegularFile RequirementKind = "regular-file"
	RequirementExecutable  RequirementKind = "executable"
)

// StepSpec is deliberately repository-neutral. Adapters resolve toolchains and
// produce exact argv; the coordinator never invokes a shell or interprets a
// language-specific package manifest.
type StepSpec struct {
	ID           string
	Executable   string
	Arguments    []string
	Environment  []string
	Directory    string
	RunDirectory string
	Timeout      time.Duration
}

type Requirement struct {
	ID   string
	Path string
	Kind RequirementKind
}

type Toolchain struct {
	ID               string
	RequestedVersion string
	ResolvedVersion  string
	Executable       string
}

type Plan struct {
	WorktreeID   string
	Adapter      string
	WorktreeRoot string
	Ownership    Ownership
	Fingerprint  string
	Steps        []StepSpec
	Requirements []Requirement
	Toolchains   []Toolchain
}

type PlanningRequest struct {
	OperationID string
	WorktreeID  string
}

type EnsureRequest struct {
	OperationID string
	WorktreeID  string
}

type OperationRecord struct {
	OperationID string
	WorktreeID  string
	State       State
	Phase       Phase
	Fingerprint string
	NextStep    int
	StepCount   int
	FailureCode string
}

type Result struct {
	WorktreeID   string
	Adapter      string
	WorktreeRoot string
	Ownership    Ownership
	State        State
	Fingerprint  string
	Toolchains   []Toolchain
	PreparedAt   time.Time
}

func (record OperationRecord) Validate() error {
	return validateRecord(record)
}

func (result Result) Validate() error {
	return validateResult(result)
}
