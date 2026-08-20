// Package action owns the generic profile-action domain: the finite action
// vocabulary, scope validation, risk confirmation rules, and the exact-command
// runner. It contains no consuming-repository identity; every concrete command
// arrives from an accepted private profile revision.
package action

import (
	"errors"
	"regexp"
	"time"
)

const (
	KindLifecycle = "lifecycle"
	KindCommand   = "command"

	ScopeMachine     = "machine"
	ScopeRepository  = "repository"
	ScopeWorktree    = "worktree"
	ScopeEnvironment = "environment"
	ScopeService     = "service"

	RiskLocal       = "local"
	RiskRemoteRead  = "remote-read"
	RiskRemoteWrite = "remote-write"

	LifecyclePrepare = "prepare"
	LifecycleStart   = "start"
	LifecycleStop    = "stop"
	LifecycleCleanup = "cleanup"

	// MaximumOutputBytes bounds each captured stream of a command action.
	MaximumOutputBytes = 1024 * 1024
	// MaximumTimeout bounds a single command action run.
	MaximumTimeout = 30 * time.Minute
)

var (
	ErrInvalidDefinition = errors.New("profile action definition is invalid")
	ErrScopeMismatch     = errors.New("profile action scope does not match the requested target")
	ErrInvalidCommand    = errors.New("profile action command is invalid")
	ErrCommandStart      = errors.New("profile action command could not start")

	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
)

// Definition is the repository-neutral projection of one accepted profile
// action. It never carries the command itself; the command is compiled only
// when the action is executed against a pinned accepted revision.
type Definition struct {
	ID          string
	DisplayName string
	Scope       string
	Risk        string
	Kind        string
	Lifecycle   string
}

// Validate enforces the finite action vocabulary.
func (definition Definition) Validate() error {
	if !identifierPattern.MatchString(definition.ID) || definition.DisplayName == "" || len(definition.DisplayName) > 256 {
		return ErrInvalidDefinition
	}
	switch definition.Scope {
	case ScopeMachine, ScopeRepository, ScopeWorktree, ScopeEnvironment, ScopeService:
	default:
		return ErrInvalidDefinition
	}
	switch definition.Risk {
	case RiskLocal, RiskRemoteRead, RiskRemoteWrite:
	default:
		return ErrInvalidDefinition
	}
	switch definition.Kind {
	case KindCommand:
		if definition.Lifecycle != "" {
			return ErrInvalidDefinition
		}
	case KindLifecycle:
		switch definition.Lifecycle {
		case LifecyclePrepare, LifecycleStart, LifecycleStop, LifecycleCleanup:
		default:
			return ErrInvalidDefinition
		}
	default:
		return ErrInvalidDefinition
	}
	return nil
}

// RequiresConfirmation reports whether every run of the action must carry an
// explicit per-run confirmation. Acceptance of the profile revision authorizes
// local and remote-read actions; remote-write actions remain high risk.
func (definition Definition) RequiresConfirmation() bool {
	return definition.Risk == RiskRemoteWrite
}

// Target names the exact resources a run addresses. IDs are opaque.
type Target struct {
	RepositoryID  string
	WorktreeID    string
	EnvironmentID string
	ServiceID     string
}

// ValidateScope checks that the requested target carries exactly the
// identifiers the action scope needs: no fewer, and no extra identifiers that
// would let a caller imply a narrower scope than the profile declared.
func ValidateScope(scope string, target Target) error {
	if target.RepositoryID == "" {
		return ErrScopeMismatch
	}
	switch scope {
	case ScopeMachine, ScopeRepository:
		if target.WorktreeID != "" || target.EnvironmentID != "" || target.ServiceID != "" {
			return ErrScopeMismatch
		}
	case ScopeWorktree:
		if target.WorktreeID == "" || target.EnvironmentID != "" || target.ServiceID != "" {
			return ErrScopeMismatch
		}
	case ScopeEnvironment:
		if target.EnvironmentID == "" || target.ServiceID != "" {
			return ErrScopeMismatch
		}
	case ScopeService:
		if target.EnvironmentID == "" || target.ServiceID == "" {
			return ErrScopeMismatch
		}
	default:
		return ErrScopeMismatch
	}
	return nil
}

// ExactCommand is a fully compiled executable invocation. There is no shell,
// no interpreter, and no inherited environment.
type ExactCommand struct {
	Executable   string
	Arguments    []string
	Directory    string
	Environment  []string
	Timeout      time.Duration
	RunDirectory string
}

// Outcome is the bounded, secret-free result of one command run.
type Outcome struct {
	ExitCode        int
	TimedOut        bool
	StdoutTruncated bool
	StderrTruncated bool
	StartedAt       time.Time
	FinishedAt      time.Time
}
