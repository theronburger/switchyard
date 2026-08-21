package configuration

import "encoding/json"

const SchemaVersion = 1

type Document struct {
	SchemaVersion   int                       `yaml:"schemaVersion" json:"schemaVersion"`
	Machine         Machine                   `yaml:"machine" json:"machine"`
	SecretProviders map[string]SecretProvider `yaml:"secretProviders" json:"secretProviders"`
	Repositories    map[string]Repository     `yaml:"repositories" json:"repositories"`
}

type Machine struct {
	Ports     PortRange        `yaml:"ports" json:"ports"`
	Execution MachineExecution `yaml:"execution" json:"execution"`
}

type PortRange struct {
	First int `yaml:"first" json:"first"`
	Last  int `yaml:"last" json:"last"`
}

type MachineExecution struct {
	InheritedEnvironment []string `yaml:"inheritedEnvironment" json:"inheritedEnvironment"`
	ShellDefault         string   `yaml:"shellDefault" json:"shellDefault"`
}

type SecretProvider struct {
	Kind string `yaml:"kind" json:"kind"`
}

// Repository contains only repository-neutral profile sections. Individual
// sections are compiled into supported primitives before a revision can be
// accepted; their data is retained here without attaching repository identity
// or commands to product code.
type Repository struct {
	Enabled            bool                         `yaml:"enabled" json:"enabled"`
	DisplayName        string                       `yaml:"displayName" json:"displayName"`
	Root               string                       `yaml:"root" json:"root"`
	Git                Git                          `yaml:"git" json:"git"`
	Values             map[string]ValueSource       `yaml:"values" json:"values"`
	Toolchains         map[string]Toolchain         `yaml:"toolchains" json:"toolchains"`
	Caches             map[string]Cache             `yaml:"caches" json:"caches"`
	EnvironmentSources map[string]EnvironmentSource `yaml:"environmentSources" json:"environmentSources"`
	Preparation        Preparation                  `yaml:"preparation" json:"preparation"`
	Targets            map[string]Target            `yaml:"targets" json:"targets"`
	DefaultTarget      string                       `yaml:"defaultTarget" json:"defaultTarget"`
	Services           map[string]Service           `yaml:"services" json:"services"`
	Infrastructure     map[string]Infrastructure    `yaml:"infrastructure" json:"infrastructure"`
	Artifacts          map[string]Artifact          `yaml:"artifacts" json:"artifacts"`
	Actions            map[string]Action            `yaml:"actions" json:"actions"`
	Cleanup            Cleanup                      `yaml:"cleanup" json:"cleanup"`
}

type ValueSource struct {
	Kind       string `yaml:"kind" json:"kind"`
	Root       string `yaml:"root" json:"root"`
	Path       string `yaml:"path" json:"path"`
	Key        string `yaml:"key" json:"key"`
	Trim       bool   `yaml:"trim" json:"trim"`
	TrimPrefix string `yaml:"trimPrefix" json:"trimPrefix"`
}

type Toolchain struct {
	RequestedVersion string   `yaml:"requestedVersion" json:"requestedVersion"`
	Executable       string   `yaml:"executable" json:"executable"`
	Provision        *Command `yaml:"provision" json:"provision,omitempty"`
}

type Cache struct {
	Directory string `yaml:"directory" json:"directory"`
}

// EnvironmentSource declares one bounded repository-owned dotenv file whose
// allowlisted entries are compiled into child process environments. The file
// is parsed as data, never sourced as shell, and only the names in Allow may
// cross into a child. Targets restricts the source to exact target IDs; an
// empty list applies it to every target. Optional permits a missing file.
type EnvironmentSource struct {
	Kind     string   `yaml:"kind" json:"kind"`
	Root     string   `yaml:"root" json:"root"`
	Path     string   `yaml:"path" json:"path"`
	Optional bool     `yaml:"optional" json:"optional"`
	Targets  []string `yaml:"targets" json:"targets"`
	Allow    []string `yaml:"allow" json:"allow"`
}

type Target struct {
	DisplayName string              `yaml:"displayName" json:"displayName"`
	Risk        string              `yaml:"risk" json:"risk"`
	WarnOnStart bool                `yaml:"warnOnStart" json:"warnOnStart"`
	Environment map[string]ValueRef `yaml:"environment" json:"environment"`
}

type Service struct {
	DisplayName       string              `yaml:"displayName" json:"displayName"`
	Kind              string              `yaml:"kind" json:"kind"`
	Available         *bool               `yaml:"available" json:"available,omitempty"`
	UnavailableReason string              `yaml:"unavailableReason" json:"unavailableReason"`
	Dependencies      []string            `yaml:"dependencies" json:"dependencies"`
	Ports             map[string]Port     `yaml:"ports" json:"ports"`
	Environment       map[string]ValueRef `yaml:"environment" json:"environment"`
	Prepare           []Command           `yaml:"prepare" json:"prepare"`
	Initialize        []Command           `yaml:"initialize" json:"initialize"`
	Command           Command             `yaml:"command" json:"command"`
	Readiness         []Probe             `yaml:"readiness" json:"readiness"`
	ReadinessTimeout  string              `yaml:"readinessTimeout" json:"readinessTimeout"`
	Health            []Probe             `yaml:"health" json:"health"`
	Infrastructure    []string            `yaml:"infrastructure" json:"infrastructure"`
	Artifacts         []string            `yaml:"artifacts" json:"artifacts"`
}

func (service Service) IsAvailable() bool {
	return service.Available == nil || *service.Available
}

type Port struct {
	Preferred []int          `yaml:"preferred" json:"preferred"`
	Publish   []PublishedURL `yaml:"publish" json:"publish"`
}

type PublishedURL struct {
	Name   string `yaml:"name" json:"name"`
	Scheme string `yaml:"scheme" json:"scheme"`
	Host   string `yaml:"host" json:"host"`
	Path   string `yaml:"path" json:"path"`
}

type Command struct {
	Executable       string              `yaml:"executable" json:"executable"`
	Arguments        []ValueRef          `yaml:"arguments" json:"arguments"`
	WorkingDirectory string              `yaml:"workingDirectory" json:"workingDirectory"`
	Environment      map[string]ValueRef `yaml:"environment" json:"environment"`
	Timeout          string              `yaml:"timeout" json:"timeout"`
}

type ValueRef struct {
	Literal      *string        `yaml:"literal" json:"literal,omitempty"`
	Segments     []ValueRef     `yaml:"segments,omitempty" json:"segments,omitempty"`
	HostHome     bool           `yaml:"hostHome,omitempty" json:"hostHome,omitempty"`
	Target       string         `yaml:"target" json:"target,omitempty"`
	Port         *PortReference `yaml:"port" json:"port,omitempty"`
	URL          *URLReference  `yaml:"url" json:"url,omitempty"`
	WorktreePath *string        `yaml:"worktreePath" json:"worktreePath,omitempty"`
	RuntimePath  *string        `yaml:"runtimePath" json:"runtimePath,omitempty"`
	Artifact     string         `yaml:"artifact" json:"artifact,omitempty"`
	Cache        string         `yaml:"cache" json:"cache,omitempty"`
	Value        string         `yaml:"value" json:"value,omitempty"`
}

type PortReference struct {
	Service string `yaml:"service" json:"service"`
	Purpose string `yaml:"purpose" json:"purpose"`
}

type URLReference struct {
	Service string `yaml:"service" json:"service"`
	Purpose string `yaml:"purpose" json:"purpose"`
	Scheme  string `yaml:"scheme" json:"scheme"`
	Host    string `yaml:"host" json:"host"`
	Path    string `yaml:"path" json:"path"`
}

type Probe struct {
	Kind             string        `yaml:"kind" json:"kind"`
	Port             string        `yaml:"port" json:"port"`
	Method           string        `yaml:"method" json:"method"`
	Path             string        `yaml:"path" json:"path"`
	AcceptedStatuses []StatusRange `yaml:"acceptedStatuses" json:"acceptedStatuses"`
}

type StatusRange struct {
	Minimum int `yaml:"minimum" json:"minimum"`
	Maximum int `yaml:"maximum" json:"maximum"`
}

type Infrastructure struct {
	Kind           string                   `yaml:"kind" json:"kind"`
	Image          string                   `yaml:"image" json:"image"`
	Environment    map[string]ValueRef      `yaml:"environment" json:"environment"`
	ContainerPorts map[string]ContainerPort `yaml:"containerPorts" json:"containerPorts"`
}

type ContainerPort struct {
	Service       string `yaml:"service" json:"service"`
	Purpose       string `yaml:"purpose" json:"purpose"`
	ContainerPort int    `yaml:"containerPort" json:"containerPort"`
}

type Artifact struct {
	Content    string     `yaml:"content" json:"content"`
	Segments   []ValueRef `yaml:"segments,omitempty" json:"segments,omitempty"`
	Filename   string     `yaml:"filename" json:"filename"`
	Executable bool       `yaml:"executable" json:"executable"`
}

type Action struct {
	DisplayName string   `yaml:"displayName" json:"displayName"`
	Scope       string   `yaml:"scope" json:"scope"`
	Risk        string   `yaml:"risk" json:"risk"`
	Command     *Command `yaml:"command" json:"command,omitempty"`
	Lifecycle   string   `yaml:"lifecycle" json:"lifecycle"`
}

type Cleanup struct {
	PreparationRetention int `yaml:"preparationRetention" json:"preparationRetention"`
}

type Preparation struct {
	Fingerprint Fingerprint       `yaml:"fingerprint" json:"fingerprint"`
	Steps       []PreparationStep `yaml:"steps" json:"steps"`
	Verify      []Verification    `yaml:"verify" json:"verify"`
}

type Fingerprint struct {
	Files []string `yaml:"files" json:"files"`
	Globs []string `yaml:"globs" json:"globs"`
}

type PreparationStep struct {
	ID               string            `yaml:"id" json:"id"`
	Executable       string            `yaml:"executable" json:"executable"`
	Arguments        []string          `yaml:"arguments" json:"arguments"`
	WorkingDirectory string            `yaml:"workingDirectory" json:"workingDirectory"`
	Environment      map[string]string `yaml:"environment" json:"environment"`
	Timeout          string            `yaml:"timeout" json:"timeout"`
}

type Verification struct {
	ID   string `yaml:"id" json:"id"`
	Kind string `yaml:"kind" json:"kind"`
	Path string `yaml:"path" json:"path"`
}

type Git struct {
	Remote               string `yaml:"remote" json:"remote"`
	DefaultBase          string `yaml:"defaultBase" json:"defaultBase"`
	ManagedWorktreesRoot string `yaml:"managedWorktreesRoot" json:"managedWorktreesRoot"`
}

type Loaded struct {
	Document          Document
	CanonicalPayload  json.RawMessage
	Digest            string
	SourceDigest      string
	RepositoryDigests map[string]string
	ExecutableDigests map[string]string
}
