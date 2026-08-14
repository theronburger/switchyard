package marketplace

type ServiceKind string

const (
	ServiceKindWeb ServiceKind = "web"
	ServiceKindAPI ServiceKind = "api"
)

type PlannedExecutable string

const RepositoryYarnExecutable PlannedExecutable = "repository-yarn"

type PlannedCommand struct {
	Executable       PlannedExecutable
	Arguments        []string
	WorkingDirectory string
}

type PortRequirement struct {
	ID                       string
	Purpose                  string
	BindHost                 string
	PreferredPort            int
	PreferredPortEnvironment string
	PreferredRelativeTo      string
	PreferredOffset          int
}

type PortAssignment struct {
	RequirementID string
	Host          string
	Port          int
}

type EnvironmentValueFormat string

const (
	EnvironmentValueDecimalPort EnvironmentValueFormat = "decimal-port"
	EnvironmentValueHTTPURL     EnvironmentValueFormat = "http-url"
)

type EnvironmentBinding struct {
	Name            string
	PortRequirement string
	Format          EnvironmentValueFormat
}

type EnvironmentVariable struct {
	Name  string
	Value string
}

type ProbeKind string

const (
	ProbeKindTCP  ProbeKind = "tcp"
	ProbeKindHTTP ProbeKind = "http"
)

type HTTPStatusRange struct {
	Minimum int
	Maximum int
}

type Probe struct {
	Kind             ProbeKind
	PortRequirement  string
	Method           string
	Path             string
	AcceptedStatuses []HTTPStatusRange
}

type InfrastructureScope string

const EnvironmentInfrastructureScope InfrastructureScope = "environment"

type ContainerPort struct {
	PortRequirement string
	ContainerPort   int
}

type InfrastructureRequirement struct {
	ID          string
	DisplayName string
	Kind        string
	Scope       InfrastructureScope
	Image       string
	Dedicated   bool
	Ports       []ContainerPort
	Readiness   []Probe
}

type OverlayValueFormat string

const (
	OverlayValueIntegerPort OverlayValueFormat = "integer-port"
	OverlayValueHTTPURL     OverlayValueFormat = "http-url"
)

type ServerlessOverride struct {
	ConfigurationPath []string
	PortRequirement   string
	Format            OverlayValueFormat
}

type ServerlessOverlay struct {
	Directory    string
	Filename     string
	SourceConfig string
	Overrides    []ServerlessOverride
}

type ServiceDefinition struct {
	ID                  string
	DisplayName         string
	Kind                ServiceKind
	WorkspacePackage    string
	PortRequirements    []PortRequirement
	PrepareCommands     []PlannedCommand
	RunCommand          PlannedCommand
	EnvironmentBindings []EnvironmentBinding
	PublishedRoutes     []EnvironmentBinding
	Readiness           []Probe
	Health              []Probe
	Infrastructure      []InfrastructureRequirement
	ServerlessOverlay   *ServerlessOverlay
}

type ServicePlan struct {
	ID                string
	DisplayName       string
	Kind              ServiceKind
	WorkspacePackage  string
	Ports             []PortAssignment
	PrepareCommands   []PlannedCommand
	RunCommand        PlannedCommand
	Environment       []EnvironmentVariable
	Readiness         []Probe
	Health            []Probe
	Infrastructure    []InfrastructureRequirement
	ServerlessOverlay *ServerlessOverlay
}

type Catalog struct {
	definitions map[string]ServiceDefinition
}
