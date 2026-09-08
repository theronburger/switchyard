package workspaces

import "time"

const APIVersion = 3

type Config struct {
	SchemaVersion int                `json:"schemaVersion"`
	Repositories  []RepositoryConfig `json:"repositories"`
	Choices       map[string]Choices `json:"choices,omitempty"`
}

type RepositoryConfig struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Path          string          `json:"path"`
	DefaultTarget string          `json:"defaultTarget"`
	Setup         []Command       `json:"setup,omitempty"`
	Targets       []Target        `json:"targets"`
	Services      []ServiceConfig `json:"services"`
	WorktreesPath string          `json:"worktreesPath,omitempty"`
}

type Command struct {
	Script         string            `json:"script"`
	Directory      string            `json:"directory,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
}

type Target struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Confirm     bool              `json:"confirm,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

type ServiceConfig struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind,omitempty"`
	Dependencies []string  `json:"dependencies,omitempty"`
	Prepare      []Command `json:"prepare,omitempty"`
	Command      Command   `json:"command"`
	Ports        []Port    `json:"ports,omitempty"`
	Readiness    Readiness `json:"readiness,omitempty"`
}

type Port struct {
	Name        string `json:"name"`
	Environment string `json:"environment,omitempty"`
	Preferred   int    `json:"preferred,omitempty"`
	URL         bool   `json:"url,omitempty"`
}

type Readiness struct {
	Port           string `json:"port,omitempty"`
	Path           string `json:"path,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

type Choices struct {
	Target   string   `json:"target"`
	Services []string `json:"services"`
}

type Snapshot struct {
	Version      int                 `json:"version"`
	InstanceID   string              `json:"instanceId"`
	Revision     uint64              `json:"revision"`
	Repositories []RepositorySummary `json:"repositories"`
	Workspaces   []Workspace         `json:"workspaces"`
}

type RepositorySummary struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Path          string           `json:"path"`
	DefaultTarget string           `json:"defaultTarget"`
	Targets       []TargetSummary  `json:"targets"`
	Services      []ServiceSummary `json:"services"`
	Error         string           `json:"error,omitempty"`
}

type TargetSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Confirm bool   `json:"confirm,omitempty"`
}

type ServiceSummary struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

type Workspace struct {
	Path         string  `json:"path"`
	RepositoryID string  `json:"repositoryId"`
	Branch       string  `json:"branch"`
	Head         string  `json:"head"`
	Primary      bool    `json:"primary"`
	Locked       bool    `json:"locked"`
	Missing      bool    `json:"missing"`
	Dirty        bool    `json:"dirty"`
	Unpushed     bool    `json:"unpushed"`
	GitError     string  `json:"gitError,omitempty"`
	Choices      Choices `json:"choices"`
	Run          *Run    `json:"run,omitempty"`
}

type RunRequest struct {
	Path            string   `json:"path"`
	RequestID       string   `json:"requestId"`
	Target          string   `json:"target"`
	Services        []string `json:"services"`
	ConfirmedTarget string   `json:"confirmedTarget,omitempty"`
	PrepareOnly     bool     `json:"prepareOnly,omitempty"`
}

type Run struct {
	ID        string            `json:"id"`
	Path      string            `json:"path"`
	Target    string            `json:"target"`
	Requested []string          `json:"requested"`
	State     string            `json:"state"`
	Step      string            `json:"step"`
	Error     string            `json:"error,omitempty"`
	StartedAt time.Time         `json:"startedAt"`
	Services  []ServiceRun      `json:"services"`
	URLs      map[string]string `json:"urls"`
}

type ServiceRun struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	PID   int    `json:"pid,omitempty"`
}

type LogOutput struct {
	RunID string `json:"runId"`
	Text  string `json:"text"`
}

type PrunePlan struct {
	Path     string   `json:"path"`
	Branch   string   `json:"branch"`
	Blockers []string `json:"blockers"`
}

type CreateRequest struct {
	RepositoryID string `json:"repositoryId"`
	Branch       string `json:"branch"`
	Base         string `json:"base,omitempty"`
}
