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
	ID          string     `json:"id"`
	DisplayName string     `json:"displayName"`
	RootPath    string     `json:"rootPath"`
	Adapter     string     `json:"adapter"`
	Remote      string     `json:"remote"`
	Worktrees   []Worktree `json:"worktrees"`
}

type Worktree struct {
	ID           string        `json:"id"`
	Path         string        `json:"path"`
	Branch       string        `json:"branch,omitempty"`
	HeadRevision string        `json:"headRevision"`
	IsPrimary    bool          `json:"isPrimary"`
	Git          WorktreeState `json:"git"`
}

type WorktreeState struct {
	HasTrackedChanges  bool `json:"hasTrackedChanges"`
	HasUntrackedFiles  bool `json:"hasUntrackedFiles"`
	HasUnpushedCommits bool `json:"hasUnpushedCommits"`
	Locked             bool `json:"locked"`
	Prunable           bool `json:"prunable"`
}

type Environment struct {
	ID                   string                `json:"id"`
	Revision             int64                 `json:"revision"`
	RepositoryID         string                `json:"repositoryId"`
	WorktreeID           string                `json:"worktreeId"`
	DisplayName          string                `json:"displayName"`
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
	ID            string      `json:"id"`
	DisplayName   string      `json:"displayName"`
	Kind          string      `json:"kind"`
	DesiredState  string      `json:"desiredState"`
	ObservedState string      `json:"observedState"`
	Health        string      `json:"health"`
	PortLeaseIDs  []string    `json:"portLeaseIds"`
	Run           *ServiceRun `json:"run,omitempty"`
}

type ServiceRun struct {
	ID           string    `json:"id"`
	StartedAt    time.Time `json:"startedAt"`
	RestartCount int       `json:"restartCount"`
	ProcessCount int       `json:"processCount"`
	CPUPercent   float64   `json:"cpuPercent"`
	MemoryBytes  int64     `json:"memoryBytes"`
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
	Kind                string         `json:"kind"`
	State               string         `json:"state"`
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

type MutationReceipt struct {
	SchemaVersion int       `json:"schemaVersion"`
	RequestID     string    `json:"requestId"`
	OperationID   string    `json:"operationId"`
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
}

type EnvironmentContext struct {
	Revision       int64             `json:"revision"`
	EnvironmentID  string            `json:"environmentId"`
	DesiredState   string            `json:"desiredState"`
	ObservedState  string            `json:"observedState"`
	Health         string            `json:"health"`
	URLs           map[string]string `json:"urls"`
	AttentionCount int               `json:"attentionCount"`
	Attention      []AttentionItem   `json:"attention"`
	Truncated      bool              `json:"truncated"`
}

type AttentionItem struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Summary  string `json:"summary"`
}
