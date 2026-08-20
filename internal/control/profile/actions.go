package profile

import (
	"path/filepath"
	"sort"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	actioncontrol "github.com/theronburger/switchyard/internal/control/action"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

// ActionDefinitions projects the accepted profile's actions into the generic
// action vocabulary, sorted by ID. Commands are deliberately not included.
func ActionDefinitions(profile configuration.Repository) ([]actioncontrol.Definition, error) {
	definitions := make([]actioncontrol.Definition, 0, len(profile.Actions))
	for id, configured := range profile.Actions {
		definition := actioncontrol.Definition{
			ID: id, DisplayName: configured.DisplayName, Scope: configured.Scope, Risk: configured.Risk,
			Kind: actioncontrol.KindCommand, Lifecycle: configured.Lifecycle,
		}
		if configured.Command == nil {
			definition.Kind = actioncontrol.KindLifecycle
		}
		if err := definition.Validate(); err != nil {
			return nil, ErrProfileInvalid
		}
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(left, right int) bool { return definitions[left].ID < definitions[right].ID })
	return definitions, nil
}

// ActionCompileRequest pins one command action run to an accepted registration.
type ActionCompileRequest struct {
	Registration Registration
	ActionID     string
	OperationID  string
	// ServiceID is the service addressed by a service-scoped action; it selects
	// the default service for port and URL references.
	ServiceID string
	// Leases are the environment's current port leases, available only to
	// environment- and service-scoped actions.
	Leases []portlease.Lease
}

// CompileAction turns an accepted command action into an exact command. It
// resolves every argument and environment value from the pinned accepted
// profile; a value that cannot be resolved fails closed.
func CompileAction(request ActionCompileRequest) (actioncontrol.ExactCommand, error) {
	registration := request.Registration
	if !validRegistration(registration) || !profileIDPattern.MatchString(request.OperationID) {
		return actioncontrol.ExactCommand{}, ErrProfileInvalid
	}
	configured, found := registration.Profile.Actions[request.ActionID]
	if !found || configured.Command == nil {
		return actioncontrol.ExactCommand{}, ErrProfileInvalid
	}
	if request.ServiceID != "" {
		if _, found := registration.Profile.Services[request.ServiceID]; !found {
			return actioncontrol.ExactCommand{}, ErrProfileInvalid
		}
	}
	leases := make(map[portlease.Key]portlease.Lease, len(request.Leases))
	if configured.Scope == actioncontrol.ScopeEnvironment || configured.Scope == actioncontrol.ScopeService {
		leases = leaseMap(request.Leases)
	}
	root := registration.WorktreeRoot
	if configured.Scope == actioncontrol.ScopeMachine || configured.Scope == actioncontrol.ScopeRepository {
		root = registration.RepositoryRoot
	}
	runRoot := filepath.Join(registration.RuntimeRoot, "actions", registration.ProfileKey, request.OperationID)
	command := *configured.Command
	arguments, err := resolveValues(registration, runRoot, request.ServiceID, command.Arguments, nil, leases)
	if err != nil {
		return actioncontrol.ExactCommand{}, err
	}
	environment, err := resolveEnvironment(registration, runRoot, request.ServiceID, nil, leases, command.Environment)
	if err != nil {
		return actioncontrol.ExactCommand{}, err
	}
	timeout, err := time.ParseDuration(command.Timeout)
	if err != nil || timeout <= 0 || timeout > actioncontrol.MaximumTimeout {
		return actioncontrol.ExactCommand{}, ErrProfileInvalid
	}
	return actioncontrol.ExactCommand{
		Executable: command.Executable, Arguments: arguments, Environment: environment,
		Directory: filepath.Join(root, command.WorkingDirectory), Timeout: timeout,
		RunDirectory: runRoot,
	}, nil
}
