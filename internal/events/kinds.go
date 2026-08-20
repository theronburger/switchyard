package events

// Audit event kinds published by the daemon. Payloads carry only opaque
// identifiers, stable codes, and lifecycle vocabulary: never commands,
// environment values, paths, credentials, or log contents.
const (
	// KindOperationCreated records that a mutation was persisted as an
	// operation before any side effect. Payload: OperationAuditPayload.
	KindOperationCreated = "operation.created"
	// KindOperationTransitioned records every operation state change,
	// including terminal failures with their stable error code.
	// Payload: OperationAuditPayload.
	KindOperationTransitioned = "operation.transitioned"
	// KindConfigurationAccepted records that the owner accepted one immutable
	// configuration revision. Payload: ConfigurationAuditPayload.
	KindConfigurationAccepted = "configuration.accepted"
	// KindOccupancyAcquired records an explicit handoff lease on a worktree.
	// Payload: OccupancyAuditPayload.
	KindOccupancyAcquired = "occupancy.acquired"
	// KindOccupancyReleased records that a handoff lease ended.
	// Payload: OccupancyAuditPayload.
	KindOccupancyReleased = "occupancy.released"
)

type OperationAuditPayload struct {
	OperationID   string `json:"operationId"`
	Kind          string `json:"kind"`
	State         string `json:"state"`
	RunID         string `json:"runId,omitempty"`
	EnvironmentID string `json:"environmentId,omitempty"`
	ErrorCode     string `json:"errorCode,omitempty"`
}

type ConfigurationAuditPayload struct {
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

type OccupancyAuditPayload struct {
	LeaseID    string `json:"leaseId"`
	WorktreeID string `json:"worktreeId"`
	HolderKind string `json:"holderKind"`
}
