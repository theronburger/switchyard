package processhost

import (
	"context"
	"errors"
	"io"
	"syscall"
	"time"
)

const (
	OwnershipSchemaVersion    = 1
	OwnershipFileName         = "ownership.json"
	LaunchIntentSchemaVersion = 1
	LaunchIntentFileName      = "launch-intent.json"
	StdoutLogFileName         = "stdout.log"
	StderrLogFileName         = "stderr.log"
	StateOrphanUnverified     = "orphaned-unverified"
)

var (
	ErrAlreadyOwned        = errors.New("run directory already has an ownership record")
	ErrOwnershipInvalid    = errors.New("process ownership record is invalid")
	ErrOwnershipMismatch   = errors.New("process identity no longer matches persisted ownership")
	ErrProcessNotFound     = errors.New("process not found")
	ErrUnstableGroup       = errors.New("process group membership changed during ownership verification")
	ErrLaunchIntentInvalid = errors.New("process launch intent is invalid")
	ErrOrphanUnverified    = errors.New("process evidence exists without verified ownership")
	ErrExitNotObservable   = errors.New("process exit is not observable by this host")
)

type LaunchSpec struct {
	EnvironmentID string
	ServiceID     string
	RunID         string
	Executable    string
	Arguments     []string
	Environment   []string
	Directory     string
	RunDirectory  string
	// Stdout and Stderr, when both are set, receive the child's streams
	// through pipes instead of the owned append-only log files. The caller
	// then owns bounding and the files at the run directory's standard log
	// paths; ownership still records those paths. Pipe drain after the
	// leader exits is bounded by the host's kill wait.
	Stdout io.Writer
	Stderr io.Writer
	// DeferReap keeps the exited leader unreaped until WaitExit or Forget is
	// called. While the leader is a zombie its PID, and therefore its group
	// ID, still positively denote this run, so a caller can first stop
	// descendants that outlived the leader and only then collect its status.
	DeferReap bool
}

type ProcessIdentity struct {
	PID                int       `json:"pid"`
	ParentPID          int       `json:"parentPid"`
	ProcessGroupID     int       `json:"processGroupId"`
	StartedAt          time.Time `json:"startedAt"`
	CommandFingerprint string    `json:"commandFingerprint"`
}

// LaunchIntent is persisted before fork. It deliberately contains no raw
// arguments or environment values; the exact executable and argv are bound by
// LaunchFingerprint. CandidateLeader is evidence only and never grants signal
// authority.
type LaunchIntent struct {
	SchemaVersion     int              `json:"schemaVersion"`
	EnvironmentID     string           `json:"environmentId"`
	ServiceID         string           `json:"serviceId"`
	RunID             string           `json:"runId"`
	Executable        string           `json:"executable"`
	LaunchFingerprint string           `json:"launchFingerprint"`
	RunDirectory      string           `json:"runDirectory"`
	CreatedAt         time.Time        `json:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt"`
	CandidateLeader   *ProcessIdentity `json:"candidateLeader,omitempty"`
}

type ExitStatus struct {
	ExitedAt time.Time `json:"exitedAt"`
	ExitCode int       `json:"exitCode"`
	Signal   int       `json:"signal,omitempty"`
}

type Ownership struct {
	SchemaVersion     int               `json:"schemaVersion"`
	EnvironmentID     string            `json:"environmentId"`
	ServiceID         string            `json:"serviceId"`
	RunID             string            `json:"runId"`
	State             string            `json:"state"`
	ProcessGroupID    int               `json:"processGroupId"`
	Leader            ProcessIdentity   `json:"leader"`
	Members           []ProcessIdentity `json:"members"`
	LaunchFingerprint string            `json:"launchFingerprint"`
	StdoutPath        string            `json:"stdoutPath"`
	StderrPath        string            `json:"stderrPath"`
	StartedAt         time.Time         `json:"startedAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	Exit              *ExitStatus       `json:"exit,omitempty"`
}

type ProcessSnapshot struct {
	Identity    ProcessIdentity
	Status      string
	MemoryBytes uint64
	CPUTime     time.Duration
}

type Observation struct {
	OwnershipPath      string
	IntentPath         string
	State              string
	OwnershipVerified  bool
	HasLaunchIntent    bool
	HasLogEvidence     bool
	HasProcessEvidence bool
	MemberCount        int
	MemoryBytes        uint64
	CPUTime            time.Duration
	ObservedAt         time.Time
}

type ProcessInspector interface {
	Inspect(ctx context.Context, pid int) (ProcessSnapshot, error)
	ListGroup(ctx context.Context, processGroupID int) ([]ProcessSnapshot, error)
}

type GroupSignaler interface {
	SignalGroup(processGroupID int, signal syscall.Signal) error
}

type Config struct {
	Inspector    ProcessInspector
	Signaler     GroupSignaler
	Now          func() time.Time
	GracePeriod  time.Duration
	KillWait     time.Duration
	PollInterval time.Duration
	// LeaderSettleDelay is how long after a start the host re-reads its own
	// unreaped leader once, so an executable image replaced during startup
	// (a shell shim, an env shebang) is persisted before any restart
	// consults the record. Zero selects the default of one second.
	LeaderSettleDelay time.Duration
}
