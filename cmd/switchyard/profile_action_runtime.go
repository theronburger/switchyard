package main

import (
	"context"
	"sort"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	actioncontrol "github.com/theronburger/switchyard/internal/control/action"
	profilecontrol "github.com/theronburger/switchyard/internal/control/profile"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

type configuredActionRepository struct {
	RepositoryID  string
	ProfileKey    string
	ProfileDigest string
	Profile       configuration.Repository
	Definitions   []actioncontrol.Definition
	// Primary is the registration used for machine- and repository-scoped
	// actions; it prefers the checkout at the repository root.
	Primary profilecontrol.Registration
}

// configuredProfileActionResolver binds profile actions to the accepted
// revision the daemon was composed from. The daemon restarts on every
// acceptance, so these registrations are immutable for its lifetime.
type configuredProfileActionResolver struct {
	repositories   map[string]configuredActionRepository
	byWorktree     map[string]profilecontrol.Registration
	byEnvironment  map[string]profilecontrol.Registration
	snapshots      daemon.StatusSource
	configuration  string
	acceptedDigest string
}

func newConfiguredProfileActionResolver(
	discovered repositoryInventory,
	registrations []profilecontrol.Registration,
	snapshots daemon.StatusSource,
	configurationPath string,
) (configuredProfileActionResolver, error) {
	resolver := configuredProfileActionResolver{
		repositories:  make(map[string]configuredActionRepository),
		byWorktree:    make(map[string]profilecontrol.Registration, len(registrations)),
		byEnvironment: make(map[string]profilecontrol.Registration, len(registrations)),
		snapshots:     snapshots, configuration: configurationPath,
		acceptedDigest: discovered.AcceptedConfigurationDigest,
	}
	sorted := append([]profilecontrol.Registration(nil), registrations...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].WorktreeID < sorted[right].WorktreeID })
	for _, registration := range sorted {
		resolver.byWorktree[registration.WorktreeID] = registration
		resolver.byEnvironment[registration.EnvironmentID] = registration
		entry, found := resolver.repositories[registration.RepositoryID]
		if !found {
			definitions, err := profilecontrol.ActionDefinitions(registration.Profile)
			if err != nil {
				return configuredProfileActionResolver{}, err
			}
			entry = configuredActionRepository{
				RepositoryID: registration.RepositoryID, ProfileKey: registration.ProfileKey,
				ProfileDigest: registration.ProfileDigest, Profile: registration.Profile,
				Definitions: definitions, Primary: registration,
			}
		}
		if registration.WorktreeRoot == registration.RepositoryRoot {
			entry.Primary = registration
		}
		resolver.repositories[registration.RepositoryID] = entry
	}
	return resolver, nil
}

func (resolver configuredProfileActionResolver) ListActions(context.Context) (contractv2.ProfileActionList, error) {
	list := contractv2.ProfileActionList{
		SchemaVersion: contractv2.SchemaVersion, AcceptedDigest: resolver.acceptedDigest,
		Actions: []contractv2.ProfileAction{},
	}
	repositoryIDs := make([]string, 0, len(resolver.repositories))
	for id := range resolver.repositories {
		repositoryIDs = append(repositoryIDs, id)
	}
	sort.Strings(repositoryIDs)
	for _, repositoryID := range repositoryIDs {
		entry := resolver.repositories[repositoryID]
		for _, definition := range entry.Definitions {
			list.Actions = append(list.Actions, contractv2.ProfileAction{
				ID: definition.ID, RepositoryID: entry.RepositoryID, ProfileKey: entry.ProfileKey,
				ProfileDigest: entry.ProfileDigest, DisplayName: definition.DisplayName,
				Scope: definition.Scope, Risk: definition.Risk, Kind: definition.Kind, Lifecycle: definition.Lifecycle,
				RequiresConfirmation: definition.RequiresConfirmation(),
			})
		}
	}
	return list, nil
}

func (resolver configuredProfileActionResolver) ResolveAction(
	_ context.Context,
	request contractv2.RunProfileActionRequest,
) (daemon.ProfileActionResolution, error) {
	desired, configurationError := configuration.LoadFile(resolver.configuration)
	if configurationError != nil || desired.Digest != resolver.acceptedDigest {
		return daemon.ProfileActionResolution{}, configuredActionError(409, "CONFIGURATION_NOT_ACCEPTED", "Validate and accept the current private configuration before running profile actions.", false)
	}
	entry, found := resolver.repositories[request.RepositoryID]
	if !found {
		return daemon.ProfileActionResolution{}, configuredActionError(404, "REPOSITORY_NOT_FOUND", "The requested repository is not configured.", false)
	}
	var definition actioncontrol.Definition
	for _, candidate := range entry.Definitions {
		if candidate.ID == request.ActionID {
			definition = candidate
			found = true
			break
		}
		found = false
	}
	if !found || definition.ID == "" {
		return daemon.ProfileActionResolution{}, configuredActionError(404, "ACTION_NOT_FOUND", "The requested action is not part of the accepted profile.", false)
	}
	if request.WorktreeID != "" {
		registration, found := resolver.byWorktree[request.WorktreeID]
		if !found || registration.RepositoryID != request.RepositoryID {
			return daemon.ProfileActionResolution{}, configuredActionError(404, "WORKTREE_NOT_FOUND", "The requested worktree is not available.", false)
		}
	}
	if request.EnvironmentID != "" {
		registration, found := resolver.byEnvironment[request.EnvironmentID]
		if !found || registration.RepositoryID != request.RepositoryID {
			return daemon.ProfileActionResolution{}, configuredActionError(404, "ENVIRONMENT_NOT_FOUND", "The requested environment is not available.", false)
		}
		if request.ServiceID != "" {
			service, found := registration.Profile.Services[request.ServiceID]
			if !found || !service.IsAvailable() {
				return daemon.ProfileActionResolution{}, configuredActionError(400, "SERVICE_NOT_SUPPORTED", "The requested service is not configured or available.", false)
			}
		}
	}
	resolution := daemon.ProfileActionResolution{
		Definition: definition, ProfileKey: entry.ProfileKey, ProfileDigest: entry.ProfileDigest,
		AcceptedDigest: resolver.acceptedDigest,
		Target: actioncontrol.Target{
			RepositoryID: request.RepositoryID, WorktreeID: request.WorktreeID,
			EnvironmentID: request.EnvironmentID, ServiceID: request.ServiceID,
		},
	}
	if definition.Kind == actioncontrol.KindLifecycle && definition.Lifecycle == actioncontrol.LifecycleStart {
		for id, service := range entry.Profile.Services {
			if service.IsAvailable() {
				resolution.StartServiceIDs = append(resolution.StartServiceIDs, id)
			}
		}
		sort.Strings(resolution.StartServiceIDs)
	}
	return resolution, nil
}

func (resolver configuredProfileActionResolver) CompileAction(
	ctx context.Context,
	resolution daemon.ProfileActionResolution,
	operationID string,
) (actioncontrol.ExactCommand, error) {
	entry, found := resolver.repositories[resolution.Target.RepositoryID]
	if !found {
		return actioncontrol.ExactCommand{}, configuredActionError(404, "REPOSITORY_NOT_FOUND", "The requested repository is not configured.", false)
	}
	registration := entry.Primary
	var leases []portlease.Lease
	switch {
	case resolution.Target.EnvironmentID != "":
		registration, found = resolver.byEnvironment[resolution.Target.EnvironmentID]
		if !found {
			return actioncontrol.ExactCommand{}, configuredActionError(404, "ENVIRONMENT_NOT_FOUND", "The requested environment is not available.", false)
		}
		current, err := resolver.currentLeases(ctx, resolution.Target.EnvironmentID)
		if err != nil {
			return actioncontrol.ExactCommand{}, configuredActionError(503, "STATUS_UNAVAILABLE", "The environment's current leases could not be observed.", true)
		}
		leases = current
	case resolution.Target.WorktreeID != "":
		registration, found = resolver.byWorktree[resolution.Target.WorktreeID]
		if !found {
			return actioncontrol.ExactCommand{}, configuredActionError(404, "WORKTREE_NOT_FOUND", "The requested worktree is not available.", false)
		}
	}
	return profilecontrol.CompileAction(profilecontrol.ActionCompileRequest{
		Registration: registration, ActionID: resolution.Definition.ID, OperationID: operationID,
		ServiceID: resolution.Target.ServiceID, Leases: leases,
	})
}

func (resolver configuredProfileActionResolver) currentLeases(ctx context.Context, environmentID string) ([]portlease.Lease, error) {
	if resolver.snapshots == nil {
		return nil, nil
	}
	snapshot, err := resolver.snapshots.ReadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	leases := make([]portlease.Lease, 0)
	for _, environment := range snapshot.Environments {
		if environment.ID != environmentID {
			continue
		}
		for _, lease := range environment.PortLeases {
			leases = append(leases, portlease.Lease{
				Key:  portlease.Key{EnvironmentID: environmentID, ServiceID: lease.ServiceID, Purpose: lease.Purpose},
				Host: lease.Host, Port: lease.Port,
			})
		}
	}
	return leases, nil
}
