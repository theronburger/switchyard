package contractv1

import "time"

const SchemaVersion = 1

type RuntimeDescriptor struct {
	SchemaVersion    int       `json:"schemaVersion"`
	Endpoint         string    `json:"endpoint"`
	DaemonInstanceID string    `json:"daemonInstanceId"`
	DaemonVersion    string    `json:"daemonVersion"`
	PID              int       `json:"pid"`
	ProcessStartedAt time.Time `json:"processStartedAt"`
	GeneratedAt      time.Time `json:"generatedAt"`
}

type Handshake struct {
	SchemaVersion           int    `json:"schemaVersion"`
	DaemonInstanceID        string `json:"daemonInstanceId"`
	DaemonVersion           string `json:"daemonVersion"`
	SupportedSchemaVersions []int  `json:"supportedSchemaVersions"`
}

type StatusSnapshot struct {
	SchemaVersion    int           `json:"schemaVersion"`
	SnapshotRevision int64         `json:"snapshotRevision"`
	GeneratedAt      time.Time     `json:"generatedAt"`
	Daemon           DaemonStatus  `json:"daemon"`
	Repositories     []Repository  `json:"repositories"`
	Environments     []Environment `json:"environments"`
	Operations       []Operation   `json:"operations"`
	Alerts           []Alert       `json:"alerts"`
}

type DaemonStatus struct {
	InstanceID string    `json:"instanceId"`
	Version    string    `json:"version"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"startedAt"`
}

type Repository struct {
	ID          string                 `json:"id"`
	DisplayName string                 `json:"displayName"`
	RootPath    string                 `json:"rootPath"`
	Adapter     string                 `json:"adapter"`
	Remote      string                 `json:"remote"`
	Worktrees   []Worktree             `json:"worktrees"`
	Runtime     *RepositoryRuntime     `json:"runtime,omitempty"`
	Observation *RepositoryObservation `json:"observation,omitempty"`
}

type RepositoryObservation struct {
	ObservedAt    *time.Time `json:"observedAt,omitempty"`
	LastAttemptAt time.Time  `json:"lastAttemptAt"`
	Stale         bool       `json:"stale"`
	ErrorCode     string     `json:"errorCode,omitempty"`
}

type RepositoryRuntime struct {
	DefaultTargetID string           `json:"defaultTargetId"`
	Targets         []RuntimeTarget  `json:"targets"`
	Services        []RuntimeService `json:"services"`
}

type RuntimeTarget struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Risk        string `json:"risk"`
	WarnOnStart bool   `json:"warnOnStart"`
}

type RuntimeService struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	Kind              string `json:"kind"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

type Worktree struct {
	ID           string                  `json:"id"`
	Path         string                  `json:"path"`
	Branch       string                  `json:"branch,omitempty"`
	HeadRevision string                  `json:"headRevision"`
	IsPrimary    bool                    `json:"isPrimary"`
	Git          WorktreeState           `json:"git"`
	Changes      *WorktreeChanges        `json:"changes,omitempty"`
	PullRequest  *PullRequestObservation `json:"pullRequest,omitempty"`
	Workspace    *WorkspaceStatus        `json:"workspace,omitempty"`
}

type WorkspaceStatus struct {
	Ownership   string               `json:"ownership"`
	State       string               `json:"state"`
	Fingerprint string               `json:"fingerprint"`
	PreparedAt  time.Time            `json:"preparedAt"`
	Toolchains  []WorkspaceToolchain `json:"toolchains"`
}

type WorkspaceToolchain struct {
	ID               string `json:"id"`
	RequestedVersion string `json:"requestedVersion"`
	ResolvedVersion  string `json:"resolvedVersion"`
}

type PullRequestObservation struct {
	Status        string       `json:"status"`
	Account       string       `json:"account,omitempty"`
	ObservedAt    *time.Time   `json:"observedAt,omitempty"`
	LastAttemptAt time.Time    `json:"lastAttemptAt"`
	Stale         bool         `json:"stale"`
	ErrorCode     string       `json:"errorCode,omitempty"`
	PullRequest   *PullRequest `json:"pullRequest,omitempty"`
}

type PullRequest struct {
	Number         int               `json:"number"`
	Title          string            `json:"title"`
	URL            string            `json:"url"`
	State          string            `json:"state"`
	Draft          bool              `json:"draft"`
	Mergeable      string            `json:"mergeable"`
	MergeState     string            `json:"mergeState"`
	ReviewDecision string            `json:"reviewDecision"`
	BaseBranch     string            `json:"baseBranch"`
	HeadBranch     string            `json:"headBranch"`
	HeadRevision   string            `json:"headRevision"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
	ClosedAt       *time.Time        `json:"closedAt,omitempty"`
	MergedAt       *time.Time        `json:"mergedAt,omitempty"`
	Checks         PullRequestChecks `json:"checks"`
}

type PullRequestChecks struct {
	State     string             `json:"state"`
	Total     int                `json:"total"`
	Passing   int                `json:"passing"`
	Failing   int                `json:"failing"`
	Pending   int                `json:"pending"`
	Skipping  int                `json:"skipping"`
	Cancelled int                `json:"cancelled"`
	Items     []PullRequestCheck `json:"items"`
}

type PullRequestCheck struct {
	Name        string     `json:"name"`
	Workflow    string     `json:"workflow"`
	State       string     `json:"state"`
	Bucket      string     `json:"bucket"`
	URL         string     `json:"url"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

type WorktreeState struct {
	HasTrackedChanges  bool `json:"hasTrackedChanges"`
	HasUntrackedFiles  bool `json:"hasUntrackedFiles"`
	HasUnpushedCommits bool `json:"hasUnpushedCommits"`
	Locked             bool `json:"locked"`
	Prunable           bool `json:"prunable"`
}

type LineChanges struct {
	Additions int64 `json:"additions"`
	Deletions int64 `json:"deletions"`
	Files     int   `json:"files"`
}

type ServiceLineChanges struct {
	ServiceID   string      `json:"serviceId"`
	Committed   LineChanges `json:"committed"`
	Uncommitted LineChanges `json:"uncommitted"`
}

type WorktreeChanges struct {
	BaseRevision      string               `json:"baseRevision"`
	Committed         LineChanges          `json:"committed"`
	Uncommitted       LineChanges          `json:"uncommitted"`
	SharedCommitted   LineChanges          `json:"sharedCommitted"`
	SharedUncommitted LineChanges          `json:"sharedUncommitted"`
	Services          []ServiceLineChanges `json:"services"`
}

type Environment struct {
	ID                   string                `json:"id"`
	Revision             int64                 `json:"revision"`
	RepositoryID         string                `json:"repositoryId"`
	WorktreeID           string                `json:"worktreeId"`
	DisplayName          string                `json:"displayName"`
	TargetID             string                `json:"targetId,omitempty"`
	DesiredState         string                `json:"desiredState"`
	ObservedState        string                `json:"observedState"`
	Health               string                `json:"health"`
	Services             []Service             `json:"services"`
	PortLeases           []PortLease           `json:"portLeases"`
	InfrastructureLeases []InfrastructureLease `json:"infrastructureLeases"`
	URLs                 map[string]string     `json:"urls"`
	Resources            ResourceUsage         `json:"resources"`
	AttentionAlertIDs    []string              `json:"attentionAlertIds"`
}

type Service struct {
	ID              string      `json:"id"`
	DisplayName     string      `json:"displayName"`
	Kind            string      `json:"kind"`
	DesiredState    string      `json:"desiredState"`
	ObservedState   string      `json:"observedState"`
	ObservationCode string      `json:"observationCode,omitempty"`
	Health          string      `json:"health"`
	PortLeaseIDs    []string    `json:"portLeaseIds"`
	Run             *ServiceRun `json:"run,omitempty"`
}

type ServiceRun struct {
	ID                      string    `json:"id"`
	StartedAt               time.Time `json:"startedAt"`
	RestartCount            int       `json:"restartCount"`
	ProcessCount            int       `json:"processCount"`
	CPUPercent              float64   `json:"cpuPercent"`
	MemoryBytes             int64     `json:"memoryBytes"`
	SourceRevision          string    `json:"sourceRevision,omitempty"`
	SourceHasTrackedChanges bool      `json:"sourceHasTrackedChanges,omitempty"`
	SourceHasUntrackedFiles bool      `json:"sourceHasUntrackedFiles,omitempty"`
	SourceObservedAt        time.Time `json:"sourceObservedAt,omitempty"`
}

type PortLease struct {
	ID         string    `json:"id"`
	ServiceID  string    `json:"serviceId"`
	Purpose    string    `json:"purpose"`
	Host       string    `json:"host"`
	Port       int       `json:"port"`
	State      string    `json:"state"`
	AcquiredAt time.Time `json:"acquiredAt"`
}

type InfrastructureLease struct {
	ID          string `json:"id"`
	ServiceID   string `json:"serviceId"`
	DisplayName string `json:"displayName"`
	Kind        string `json:"kind"`
	Scope       string `json:"scope"`
	State       string `json:"state"`
	Ownership   string `json:"ownership"`
}

type ResourceUsage struct {
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryBytes int64   `json:"memoryBytes"`
}

type Operation struct {
	ID                  string         `json:"id"`
	RunID               string         `json:"runId,omitempty"`
	Kind                string         `json:"kind"`
	State               string         `json:"state"`
	Phase               string         `json:"phase,omitempty"`
	EnvironmentID       string         `json:"environmentId,omitempty"`
	EnvironmentRevision int64          `json:"environmentRevision,omitempty"`
	CreatedAt           time.Time      `json:"createdAt"`
	UpdatedAt           time.Time      `json:"updatedAt"`
	Error               *ContractError `json:"error,omitempty"`
}

type Alert struct {
	ID            string    `json:"id"`
	EnvironmentID string    `json:"environmentId,omitempty"`
	ServiceID     string    `json:"serviceId,omitempty"`
	Severity      string    `json:"severity"`
	Code          string    `json:"code"`
	Summary       string    `json:"summary"`
	Status        string    `json:"status"`
	FirstSeenAt   time.Time `json:"firstSeenAt"`
	LastSeenAt    time.Time `json:"lastSeenAt"`
	Occurrences   int       `json:"occurrences"`
}

type MutationRequest struct {
	SchemaVersion               int    `json:"schemaVersion"`
	RequestID                   string `json:"requestId"`
	IdempotencyKey              string `json:"idempotencyKey"`
	ExpectedEnvironmentRevision *int64 `json:"expectedEnvironmentRevision,omitempty"`
}

type StartEnvironmentRequest struct {
	MutationRequest
	WorktreeID        string   `json:"worktreeId"`
	TargetID          string   `json:"targetId,omitempty"`
	ConfirmedTargetID string   `json:"confirmedTargetId,omitempty"`
	ServiceIDs        []string `json:"serviceIds"`
}

type StopEnvironmentRequest struct {
	MutationRequest
}

type CreateWorktreeRequest struct {
	MutationRequest
	RepositoryID string `json:"repositoryId"`
	Branch       string `json:"branch"`
	StartPoint   string `json:"startPoint,omitempty"`
}

type ArchiveWorktreeRequest struct {
	MutationRequest
	WorktreeID string `json:"worktreeId"`
}

type AdoptWorktreeRequest struct {
	MutationRequest
	WorktreeID string `json:"worktreeId"`
}

type PrepareWorktreeRequest struct {
	MutationRequest
	WorktreeID string `json:"worktreeId"`
}

type ConfigurationValidationRequest struct {
	SchemaVersion    int   `json:"schemaVersion"`
	ExpectedRevision int64 `json:"expectedRevision"`
}

type ConfigurationAcceptanceRequest struct {
	SchemaVersion    int    `json:"schemaVersion"`
	ExpectedRevision int64  `json:"expectedRevision"`
	Digest           string `json:"digest"`
}

type ConfigurationCandidate struct {
	SchemaVersion     int               `json:"schemaVersion"`
	Digest            string            `json:"digest"`
	SourceDigest      string            `json:"sourceDigest"`
	CompilerVersion   string            `json:"compilerVersion"`
	RepositoryDigests map[string]string `json:"repositoryDigests"`
	StagedAt          time.Time         `json:"stagedAt"`
}

type ConfigurationStatus struct {
	SchemaVersion    int                     `json:"schemaVersion"`
	State            string                  `json:"state"`
	AcceptedRevision int64                   `json:"acceptedRevision"`
	AcceptedDigest   string                  `json:"acceptedDigest,omitempty"`
	Candidate        *ConfigurationCandidate `json:"candidate,omitempty"`
}

type CleanupScope struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

type CleanupPlanRequest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Scope         CleanupScope `json:"scope"`
}

type CleanupApplyRequest struct {
	SchemaVersion    int      `json:"schemaVersion"`
	PlanID           string   `json:"planId"`
	ExpectedRevision int64    `json:"expectedRevision"`
	CandidateIDs     []string `json:"candidateIds"`
}

type CleanupCandidate struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	ProfileKey  string `json:"profileKey"`
	WorktreeID  string `json:"worktreeId"`
	Fingerprint string `json:"fingerprint"`
	Bytes       int64  `json:"bytes"`
	Path        string `json:"path"`
}

type CleanupProtection struct {
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Reason     string `json:"reason"`
	ProfileKey string `json:"profileKey,omitempty"`
	WorktreeID string `json:"worktreeId,omitempty"`
}

type CleanupPlan struct {
	SchemaVersion int                 `json:"schemaVersion"`
	ID            string              `json:"id"`
	Revision      int64               `json:"revision"`
	Scope         CleanupScope        `json:"scope"`
	Candidates    []CleanupCandidate  `json:"candidates"`
	Protected     []CleanupProtection `json:"protected"`
	CreatedAt     time.Time           `json:"createdAt"`
	ExpiresAt     time.Time           `json:"expiresAt"`
}

type CleanupRemoval struct {
	CandidateID string `json:"candidateId"`
	Removed     bool   `json:"removed"`
	Reason      string `json:"reason,omitempty"`
}

type CleanupResult struct {
	SchemaVersion int              `json:"schemaVersion"`
	PlanID        string           `json:"planId"`
	PlanRevision  int64            `json:"planRevision"`
	Removals      []CleanupRemoval `json:"removals"`
	CompletedAt   time.Time        `json:"completedAt"`
}

type MutationReceipt struct {
	SchemaVersion int       `json:"schemaVersion"`
	RequestID     string    `json:"requestId"`
	OperationID   string    `json:"operationId"`
	RunID         string    `json:"runId,omitempty"`
	AcceptedAt    time.Time `json:"acceptedAt"`
	EnvironmentID string    `json:"environmentId,omitempty"`
}

type ContractError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	Retryable      bool   `json:"retryable"`
	ResourceKind   string `json:"resourceKind,omitempty"`
	ResourceID     string `json:"resourceId,omitempty"`
	CurrentState   string `json:"currentState,omitempty"`
	RequestedState string `json:"requestedState,omitempty"`
	Phase          string `json:"phase,omitempty"`
	Step           string `json:"step,omitempty"`
	Diagnostic     string `json:"diagnostic,omitempty"`
	LogReference   string `json:"logReference,omitempty"`
	NextAction     string `json:"nextAction,omitempty"`
	ExitCode       *int   `json:"exitCode,omitempty"`
}

type OperationDiagnostics struct {
	SchemaVersion int                   `json:"schemaVersion"`
	OperationID   string                `json:"operationId"`
	EnvironmentID string                `json:"environmentId"`
	LogReference  string                `json:"logReference"`
	Excerpts      []OperationLogExcerpt `json:"excerpts"`
}

type OperationLogExcerpt struct {
	Stream    string `json:"stream"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Redacted  bool   `json:"redacted"`
}

type EnvironmentContext struct {
	Revision       int64               `json:"revision"`
	EnvironmentID  string              `json:"environmentId"`
	RunID          string              `json:"runId,omitempty"`
	SourceRevision string              `json:"sourceRevision,omitempty"`
	SourceDirty    bool                `json:"sourceDirty,omitempty"`
	DesiredState   string              `json:"desiredState"`
	ObservedState  string              `json:"observedState"`
	Health         string              `json:"health"`
	URLs           map[string]string   `json:"urls"`
	AttentionCount int                 `json:"attentionCount"`
	Attention      []AttentionItem     `json:"attention"`
	PullRequest    *PullRequestContext `json:"pullRequest,omitempty"`
	Truncated      bool                `json:"truncated"`
}

type PullRequestContext struct {
	Number           int    `json:"number"`
	URL              string `json:"url"`
	State            string `json:"state"`
	Draft            bool   `json:"draft"`
	Mergeable        string `json:"mergeable"`
	ReviewDecision   string `json:"reviewDecision"`
	ChecksState      string `json:"checksState"`
	HeadMatchesLocal bool   `json:"headMatchesLocal"`
	Stale            bool   `json:"stale"`
}

type AttentionItem struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Summary  string `json:"summary"`
}
