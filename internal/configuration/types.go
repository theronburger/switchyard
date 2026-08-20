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
	Enabled            bool           `yaml:"enabled" json:"enabled"`
	DisplayName        string         `yaml:"displayName" json:"displayName"`
	Root               string         `yaml:"root" json:"root"`
	Git                Git            `yaml:"git" json:"git"`
	Values             map[string]any `yaml:"values" json:"values"`
	Toolchains         map[string]any `yaml:"toolchains" json:"toolchains"`
	Caches             map[string]any `yaml:"caches" json:"caches"`
	EnvironmentSources map[string]any `yaml:"environmentSources" json:"environmentSources"`
	Preparation        Preparation    `yaml:"preparation" json:"preparation"`
	Targets            map[string]any `yaml:"targets" json:"targets"`
	DefaultTarget      string         `yaml:"defaultTarget" json:"defaultTarget"`
	Services           map[string]any `yaml:"services" json:"services"`
	Infrastructure     map[string]any `yaml:"infrastructure" json:"infrastructure"`
	Artifacts          map[string]any `yaml:"artifacts" json:"artifacts"`
	Actions            map[string]any `yaml:"actions" json:"actions"`
	Cleanup            map[string]any `yaml:"cleanup" json:"cleanup"`
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
}
