package containerhost

import "time"

type ResourceKind string

const (
	ResourceContainer ResourceKind = "container"
	ResourceVolume    ResourceKind = "volume"
	ResourceNetwork   ResourceKind = "network"
)

func (kind ResourceKind) Valid() bool {
	return kind == ResourceContainer || kind == ResourceVolume || kind == ResourceNetwork
}

type PortProtocol string

const (
	LoopbackHostIPv4              = "127.0.0.1"
	PortProtocolTCP  PortProtocol = "tcp"
)

type PortBinding struct {
	Host          string
	HostPort      int
	ContainerPort int
	Protocol      PortProtocol
}

type Resource struct {
	Kind                  ResourceKind
	ID                    string
	Name                  string
	Image                 string
	PortBindings          []PortBinding
	PublishedPortBindings []PortBinding
	State                 string
	Running               bool
	SizeBytes             int64
	Labels                map[string]string
	Ownership             Ownership
	Identity              Identity
}

type DuplicateIdentity struct {
	Kind        ResourceKind
	Identity    Identity
	ResourceIDs []string
}

type Inventory struct {
	Revision     string
	Resources    []Resource
	Duplicates   []DuplicateIdentity
	OwnedBytes   int64
	ForeignBytes int64
}

type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
	DesiredAbsent  DesiredState = "absent"
)

type Goal struct {
	Kind         ResourceKind
	Name         string
	Image        string
	PortBindings []PortBinding
	Environment  []string
	Identity     Identity
	DesiredState DesiredState
}

type ActionKind string

const (
	ActionPull   ActionKind = "pull"
	ActionCreate ActionKind = "create"
	ActionStart  ActionKind = "start"
	ActionStop   ActionKind = "stop"
	ActionRemove ActionKind = "remove"
)

type Action struct {
	Kind         ActionKind
	ResourceKind ResourceKind
	ResourceID   string
	ResourceName string
	Image        string
	PortBindings []PortBinding
	Environment  []string
	Identity     Identity
	Command      Command
}

func (action Action) Destructive() bool {
	return action.Kind == ActionStop || action.Kind == ActionRemove
}

type ProtectionCode string

const (
	ProtectionForeignCollision  ProtectionCode = "FOREIGN_RESOURCE_COLLISION"
	ProtectionUnsafeLabels      ProtectionCode = "UNSAFE_OWNERSHIP_LABELS"
	ProtectionDuplicateIdentity ProtectionCode = "DUPLICATE_OWNERSHIP_IDENTITY"
	ProtectionImmutableMismatch ProtectionCode = "OWNED_RESOURCE_IMMUTABLE_MISMATCH"
)

type Protection struct {
	Code         ProtectionCode
	ResourceKind ResourceKind
	ResourceName string
	Summary      string
}

type Plan struct {
	SchemaVersion int
	BaseRevision  string
	GeneratedAt   time.Time
	Actions       []Action
	Protections   []Protection
}
