package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	actioncontrol "github.com/theronburger/switchyard/internal/control/action"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	profilecontrol "github.com/theronburger/switchyard/internal/control/profile"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/health"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
	"github.com/theronburger/switchyard/internal/state"
)

type configuredEnvironment struct {
	EnvironmentID string
	RepositoryID  string
	Worktree      contractv1.Worktree
	ProfileKey    string
	ProfileDigest string
	Profile       configuration.Repository
}

type configuredActionResolver struct {
	byWorktree        map[string]configuredEnvironment
	byEnvironment     map[string]configuredEnvironment
	source            profilecontrol.SourceReader
	configurationPath string
	acceptedDigest    string
}

func buildConfiguredProfileRuntime(ctx context.Context, store *state.Store, paths applicationPaths, instanceID string, discovered repositoryInventory, restart func()) (*environmentRuntime, error) {
	runtimeRoot := filepath.Join(paths.root, "runtime")
	cacheRoot := filepath.Join(paths.root, "caches")
	for _, directory := range []string{runtimeRoot, cacheRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	if len(discovered.Repositories) == 0 {
		journal, err := state.NewEnvironmentJournal(store, configuredEnvironmentProjector(nil))
		if err != nil {
			return nil, err
		}
		current, err := journal.ListCurrent(ctx, "", 1)
		if err != nil {
			return nil, err
		}
		incomplete, err := journal.Incomplete(ctx)
		if err != nil {
			return nil, err
		}
		if len(current.Results) != 0 || current.HasMore || len(incomplete) != 0 {
			return nil, errors.New("persisted environments cannot be recovered without an accepted repository profile")
		}
		return &environmentRuntime{}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	manager, err := newManagedWorkspaceManager(paths, discovered)
	if err != nil {
		return nil, err
	}
	environments := make([]configuredEnvironment, 0)
	registrations := make([]profilecontrol.Registration, 0)
	workspaceRegistrations := make([]workspacecontrol.ProfileRegistration, 0)
	for _, repository := range discovered.Repositories {
		profile, found := discovered.Profiles[repository.ID]
		if !found || !profile.Enabled {
			continue
		}
		profileKey := discovered.ProfileKeys[repository.ID]
		profileDigest := discovered.ProfileDigests[repository.ID]
		for _, worktree := range repository.Worktrees {
			if worktree.Git.Prunable {
				continue
			}
			environmentID := stableConfiguredEnvironmentID(profileKey, worktree.ID)
			worktreeRoot := filepath.Join(runtimeRoot, "repositories", profileKey, worktree.ID)
			homeDirectory := filepath.Join(worktreeRoot, "home")
			temporaryDirectory := filepath.Join(worktreeRoot, "tmp")
			for _, directory := range []string{worktreeRoot, homeDirectory, temporaryDirectory} {
				if err := os.MkdirAll(directory, 0o700); err != nil {
					return nil, err
				}
			}
			executablePath := configuredExecutablePath(home, profile)
			values, err := profilecontrol.ReadValues(profile, repository.RootPath, worktree.Path)
			if err != nil {
				return nil, err
			}
			metadata := configuredEnvironment{
				EnvironmentID: environmentID, RepositoryID: repository.ID, Worktree: worktree,
				ProfileKey: profileKey, ProfileDigest: profileDigest, Profile: profile,
			}
			environments = append(environments, metadata)
			registrations = append(registrations, profilecontrol.Registration{
				EnvironmentID: environmentID, RepositoryID: repository.ID, WorktreeID: worktree.ID,
				ProfileKey: profileKey, ProfileDigest: profileDigest, RepositoryRoot: repository.RootPath,
				WorktreeRoot: worktree.Path, RuntimeRoot: runtimeRoot, CacheRoot: cacheRoot,
				HomeDirectory: homeDirectory, HostHomeDirectory: home, TemporaryDirectory: temporaryDirectory,
				ExecutablePath: executablePath, DaemonInstanceID: instanceID,
				Values: values, Profile: profile,
			})
			if len(profile.Preparation.Steps) != 0 {
				ownership := workspacecontrol.OwnershipAdopted
				if manager.Owns(repository.ID, worktree.Path) {
					ownership = workspacecontrol.OwnershipManaged
				}
				workspaceRegistrations = append(workspaceRegistrations, workspacecontrol.ProfileRegistration{
					WorktreeID: worktree.ID, WorktreeRoot: worktree.Path, ProfileKey: profileKey,
					ProfileDigest: profileDigest, RuntimeRoot: runtimeRoot, Ownership: ownership,
					Preparation: profile.Preparation,
				})
			}
		}
	}
	journal, err := state.NewEnvironmentJournal(store, configuredEnvironmentProjector(environments))
	if err != nil {
		return nil, err
	}
	current, err := journal.ListCurrent(ctx, "", 1)
	if err != nil {
		return nil, err
	}
	incomplete, err := journal.Incomplete(ctx)
	if err != nil {
		return nil, err
	}
	if len(registrations) == 0 {
		if len(current.Results) != 0 || current.HasMore || len(incomplete) != 0 {
			return nil, errors.New("persisted environments cannot be recovered without an accepted repository profile")
		}
		return &environmentRuntime{}, nil
	}
	registry, err := profilecontrol.NewRegistry(registrations)
	if err != nil {
		return nil, err
	}
	workspaceJournal, err := state.NewWorkspaceJournal(store)
	if err != nil {
		return nil, err
	}
	var workspaceCoordinator *workspacecontrol.Coordinator
	if len(workspaceRegistrations) != 0 {
		workspacePlanner, err := workspacecontrol.NewProfilePlanBuilder(workspaceRegistrations)
		if err != nil {
			return nil, err
		}
		workspaceCoordinator, err = workspacecontrol.NewCoordinator(workspacecontrol.Config{
			Journal: workspaceJournal, Planner: workspacePlanner,
			Runner: workspacecontrol.ExactStepRunner{RuntimeRoot: runtimeRoot}, Verifier: workspacecontrol.OSRequirementVerifier{},
		})
		if err != nil {
			return nil, err
		}
		if err := workspaceCoordinator.Reconcile(ctx); err != nil {
			return nil, err
		}
	}
	ports, err := portlease.NewAllocator(portlease.Config{
		Host: "127.0.0.1", FirstPort: configuredFirstPort(discovered), LastPort: configuredLastPort(discovered),
	})
	if err != nil {
		return nil, err
	}
	if err := restoreEnvironmentLeases(ctx, journal, ports); err != nil {
		return nil, err
	}
	var infrastructure environmentcontrol.InfrastructureHost
	if configuredProfilesUseInfrastructure(discovered) {
		docker, err := configuredDockerExecutable()
		if err != nil {
			return nil, err
		}
		dockerRunner := containerhost.OSRunner{}
		dockerInventory := containerhost.DockerInventory{Runner: dockerRunner, DockerBinary: docker}
		infrastructure = environmentcontrol.ContainerInfrastructureHost{
			Resources: dockerInventory, Planner: containerhost.Planner{DockerBinary: docker},
			Applier: containerhost.Reconciler{Runner: dockerRunner, Resources: dockerInventory, DockerBinary: docker},
		}
	}
	prober, err := health.NewProber(health.ProberConfig{})
	if err != nil {
		return nil, err
	}
	readiness, err := profilecontrol.NewReadinessChecker(registry, prober)
	if err != nil {
		return nil, err
	}
	coordinator, err := environmentcontrol.NewCoordinator(environmentcontrol.Config{
		Journal: journal, Ports: ports, Planner: profilecontrol.NewPlanBuilder(registry),
		Preparations: profilecontrol.FiniteRunner{}, Projections: profilecontrol.NewArtifactMaterializer(registry),
		Infrastructure: infrastructure,
		Processes:      processhost.New(processhost.Config{}), Readiness: readiness, RollbackTimeout: 45 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	outcomes, err := coordinator.Reconcile(ctx)
	if err != nil {
		return nil, err
	}
	for _, outcome := range outcomes {
		if outcome.Err != nil {
			return nil, errors.New("an interrupted environment could not be recovered safely")
		}
	}
	observer, err := environmentcontrol.NewLiveObserver(environmentcontrol.LiveObserverConfig{Coordinator: coordinator})
	if err != nil {
		return nil, err
	}
	initialContext, cancel := context.WithTimeout(ctx, environmentcontrol.DefaultLiveObservationTimeout)
	err = observer.RefreshOnce(initialContext)
	cancel()
	if err != nil {
		return nil, err
	}
	resolver := newConfiguredActionResolver(environments, paths.configuration, discovered.AcceptedConfigurationDigest)
	actions, err := daemon.NewEnvironmentActionService(daemon.EnvironmentActionServiceConfig{
		Lifecycle: ctx, Store: store, Journal: journal, Coordinator: coordinator, Workspace: workspaceCoordinator, Resolver: resolver,
	})
	if err != nil {
		return nil, err
	}
	var workspaceActions *daemon.WorkspaceActionService
	if workspaceCoordinator != nil {
		workspaceResolver := newManagedWorkspaceResolver(store, discovered)
		workspaceResolver.configurationPath = paths.configuration
		workspaceResolver.acceptedConfigurationDigest = discovered.AcceptedConfigurationDigest
		workspaceActions, err = daemon.NewWorkspaceActionService(daemon.WorkspaceActionServiceConfig{
			Lifecycle: ctx, Store: store, Backend: manager, Ensurer: workspaceCoordinator,
			Resolver: workspaceResolver, Restart: restart,
		})
		if err != nil {
			return nil, err
		}
	}
	actionResolver, err := newConfiguredProfileActionResolver(discovered, registrations, store, paths.configuration)
	if err != nil {
		return nil, err
	}
	profileActionConfig := daemon.ProfileActionServiceConfig{
		Lifecycle: ctx, Store: store, Resolver: actionResolver, Runner: actioncontrol.ExactRunner{},
		Environment: actions,
	}
	if workspaceActions != nil {
		profileActionConfig.Workspace = workspaceActions
	}
	profileActions, err := daemon.NewProfileActionService(profileActionConfig)
	if err != nil {
		return nil, err
	}
	observerDone := make(chan error, 1)
	go func() { observerDone <- observer.Run(ctx) }()
	return &environmentRuntime{
		actions: actions, workspaceActions: workspaceActions, profileActions: profileActions, observerDone: observerDone,
	}, nil
}

func configuredProfilesUseInfrastructure(discovered repositoryInventory) bool {
	for _, profile := range discovered.Profiles {
		if profile.Enabled && len(profile.Infrastructure) != 0 {
			return true
		}
	}
	return false
}

func newConfiguredActionResolver(environments []configuredEnvironment, configurationPath, acceptedDigest string) configuredActionResolver {
	resolver := configuredActionResolver{
		byWorktree:        make(map[string]configuredEnvironment, len(environments)),
		byEnvironment:     make(map[string]configuredEnvironment, len(environments)),
		source:            profilecontrol.SourceReader{GitExecutable: configuredGitExecutable()},
		configurationPath: configurationPath, acceptedDigest: acceptedDigest,
	}
	for _, environment := range environments {
		resolver.byWorktree[environment.Worktree.ID] = environment
		resolver.byEnvironment[environment.EnvironmentID] = environment
	}
	return resolver
}

func (resolver configuredActionResolver) ResolveStart(ctx context.Context, request contractv1.StartEnvironmentRequest) (daemon.EnvironmentStartResolution, error) {
	desired, configurationError := configuration.LoadFile(resolver.configurationPath)
	if configurationError != nil || desired.Digest != resolver.acceptedDigest {
		return daemon.EnvironmentStartResolution{}, configuredActionError(409, "CONFIGURATION_NOT_ACCEPTED", "Validate and accept the current private configuration before starting new work.", false)
	}
	registered, found := resolver.byWorktree[request.WorktreeID]
	if !found {
		return daemon.EnvironmentStartResolution{}, configuredActionError(404, "WORKTREE_NOT_FOUND", "The requested worktree is not available.", false)
	}
	targetID := request.TargetID
	if targetID == "" {
		targetID = registered.Profile.DefaultTarget
	}
	target, found := registered.Profile.Targets[targetID]
	if !found {
		return daemon.EnvironmentStartResolution{}, configuredActionError(400, "TARGET_NOT_SUPPORTED", "The requested target is not configured.", false)
	}
	if request.ConfirmedTargetID != "" && request.ConfirmedTargetID != targetID {
		return daemon.EnvironmentStartResolution{}, configuredActionError(409, "TARGET_CONFIRMATION_MISMATCH", "The target confirmation does not match the requested target.", false)
	}
	if target.WarnOnStart && request.ConfirmedTargetID != targetID {
		return daemon.EnvironmentStartResolution{}, configuredActionError(409, "TARGET_CONFIRMATION_REQUIRED", "This target requires confirmation for every start.", false)
	}
	selected, err := configuredServices(registered.Profile, request.ServiceIDs)
	if err != nil {
		return daemon.EnvironmentStartResolution{}, configuredActionError(400, "SERVICE_NOT_SUPPORTED", "A requested service is not configured or available.", false)
	}
	reservations := make([]portlease.Reservation, 0)
	for _, serviceID := range selected {
		service := registered.Profile.Services[serviceID]
		purposes := make([]string, 0, len(service.Ports))
		for purpose := range service.Ports {
			purposes = append(purposes, purpose)
		}
		sort.Strings(purposes)
		for _, purpose := range purposes {
			reservations = append(reservations, portlease.Reservation{
				Key:            portlease.Key{EnvironmentID: registered.EnvironmentID, ServiceID: serviceID, Purpose: purpose},
				PreferredPorts: append([]int(nil), service.Ports[purpose].Preferred...),
			})
		}
	}
	source, err := resolver.source.Read(ctx, registered.Worktree.Path)
	if err != nil {
		return daemon.EnvironmentStartResolution{}, configuredActionError(503, "SOURCE_OBSERVATION_UNAVAILABLE", "The worktree source state could not be observed.", true)
	}
	return daemon.EnvironmentStartResolution{
		EnvironmentID: registered.EnvironmentID, WorktreeID: registered.Worktree.ID, Ports: reservations,
		Intent: environmentcontrol.PlanIntent{Adapter: registered.ProfileDigest, TargetID: targetID, ServiceIDs: selected}, Source: source,
	}, nil
}

func (resolver configuredActionResolver) ResolveStop(_ context.Context, environmentID string, _ contractv1.StopEnvironmentRequest) error {
	if _, found := resolver.byEnvironment[environmentID]; !found {
		return configuredActionError(404, "ENVIRONMENT_NOT_FOUND", "The requested environment is not available.", false)
	}
	return nil
}

func configuredActionError(status int, code, message string, retryable bool) error {
	return &daemon.ActionError{Status: status, Contract: contractv1.ContractError{Code: code, Message: message, Retryable: retryable}}
}

func configuredServices(profile configuration.Repository, requested []string) ([]string, error) {
	selected := make(map[string]struct{}, len(requested))
	var add func(string) error
	add = func(id string) error {
		service, found := profile.Services[id]
		if !found || !service.IsAvailable() {
			return errors.New("service unavailable")
		}
		if _, exists := selected[id]; exists {
			return nil
		}
		for _, dependency := range service.Dependencies {
			if err := add(dependency); err != nil {
				return err
			}
		}
		selected[id] = struct{}{}
		return nil
	}
	for _, id := range requested {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func configuredEnvironmentProjector(environments []configuredEnvironment) state.EnvironmentProjector {
	byID := make(map[string]configuredEnvironment, len(environments))
	for _, environment := range environments {
		byID[environment.EnvironmentID] = environment
	}
	return func(current *contractv1.Environment, result environmentcontrol.EnvironmentResult) (contractv1.Environment, error) {
		metadata, found := byID[result.EnvironmentID]
		if !found {
			return contractv1.Environment{}, errors.New("environment metadata is unavailable")
		}
		desired, observed := projectedLifecycleStates(result.State)
		projected := contractv1.Environment{
			ID: result.EnvironmentID, RepositoryID: metadata.RepositoryID, WorktreeID: metadata.Worktree.ID,
			DisplayName: environmentDisplayName(metadata.Worktree), TargetID: result.TargetID,
			DesiredState: desired, ObservedState: observed, Health: "unknown",
			Services: []contractv1.Service{}, PortLeases: []contractv1.PortLease{}, InfrastructureLeases: []contractv1.InfrastructureLease{},
			URLs: map[string]string{}, AttentionAlertIDs: []string{},
		}
		if current != nil {
			projected.AttentionAlertIDs = append([]string(nil), current.AttentionAlertIDs...)
		}
		leasesByService := make(map[string][]string)
		for _, lease := range result.Ports {
			leaseID := stablePortLeaseID(lease.Key)
			acquiredAt := result.UpdatedAt
			if current != nil {
				for _, previous := range current.PortLeases {
					if previous.ID == leaseID {
						acquiredAt = previous.AcquiredAt
					}
				}
			}
			projected.PortLeases = append(projected.PortLeases, contractv1.PortLease{
				ID: leaseID, ServiceID: lease.Key.ServiceID, Purpose: lease.Key.Purpose,
				Host: lease.Host, Port: lease.Port, State: "leased", AcquiredAt: acquiredAt,
			})
			leasesByService[lease.Key.ServiceID] = append(leasesByService[lease.Key.ServiceID], leaseID)
			for _, published := range metadata.Profile.Services[lease.Key.ServiceID].Ports[lease.Key.Purpose].Publish {
				host := lease.Host
				if published.Host == "localhost" {
					host = "localhost"
				}
				projected.URLs[published.Name] = published.Scheme + "://" + host + ":" + strconv.Itoa(lease.Port) + published.Path
			}
		}
		results := make(map[string]environmentcontrol.ServiceResult, len(result.Services))
		serviceIDs := make(map[string]struct{})
		for _, service := range result.Services {
			results[service.ID] = service
			serviceIDs[service.ID] = struct{}{}
		}
		for serviceID := range leasesByService {
			serviceIDs[serviceID] = struct{}{}
		}
		if len(serviceIDs) == 0 && current != nil {
			for _, service := range current.Services {
				serviceIDs[service.ID] = struct{}{}
			}
		}
		ids := make([]string, 0, len(serviceIDs))
		for id := range serviceIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		allHealthy := len(ids) > 0 && result.State == domain.EnvironmentRunning
		anyUnhealthy, degraded := false, false
		for _, id := range ids {
			definition, found := metadata.Profile.Services[id]
			if !found {
				return contractv1.Environment{}, errors.New("service metadata is unavailable")
			}
			serviceResult, running := results[id]
			service := contractv1.Service{
				ID: id, DisplayName: definition.DisplayName, Kind: definition.Kind,
				DesiredState: desired, ObservedState: observed, Health: "unknown", PortLeaseIDs: append([]string(nil), leasesByService[id]...),
			}
			if running {
				if serviceResult.Observation.State != "" {
					service.ObservedState = serviceResult.Observation.State
				}
				service.Health = serviceResult.Health.Health
				if service.Health == "" {
					service.Health = "unknown"
				}
				service.ObservationCode = serviceResult.Observation.Code
				processCount := len(serviceResult.Process.Members)
				if !serviceResult.Observation.ObservedAt.IsZero() {
					processCount = serviceResult.Observation.ProcessCount
				}
				service.Run = &contractv1.ServiceRun{
					ID: result.RunID, StartedAt: serviceResult.Process.StartedAt, ProcessCount: processCount,
					CPUPercent: serviceResult.Observation.CPUPercent, MemoryBytes: serviceResult.Observation.MemoryBytes,
				}
				if result.Source != nil {
					service.Run.SourceRevision = result.Source.Revision
					service.Run.SourceHasTrackedChanges = result.Source.HasTrackedChanges
					service.Run.SourceHasUntrackedFiles = result.Source.HasUntrackedFiles
					service.Run.SourceObservedAt = result.Source.ObservedAt
				}
				projected.Resources.MemoryBytes = saturatingResourceAdd(projected.Resources.MemoryBytes, serviceResult.Observation.MemoryBytes)
				projected.Resources.CPUPercent += serviceResult.Observation.CPUPercent
				if service.ObservedState != string(domain.EnvironmentRunning) || service.ObservationCode != "" {
					degraded = true
				}
			}
			if service.Health != "healthy" {
				allHealthy = false
			}
			if service.Health == "unhealthy" {
				anyUnhealthy = true
			}
			projected.Services = append(projected.Services, service)
		}
		if projected.Resources.CPUPercent > 100 {
			projected.Resources.CPUPercent = 100
		}
		if allHealthy {
			projected.Health = "healthy"
		} else if anyUnhealthy {
			projected.Health = "unhealthy"
		} else if degraded || result.State == domain.EnvironmentRunning || result.State == domain.EnvironmentFailed || result.State == domain.EnvironmentOrphaned {
			projected.Health = "degraded"
		}
		for _, goal := range result.Infrastructure {
			projected.InfrastructureLeases = append(projected.InfrastructureLeases, contractv1.InfrastructureLease{
				ID: stableInfrastructureLeaseID(goal.Identity), ServiceID: goal.Identity.ServiceID,
				DisplayName: goal.Name, Kind: string(goal.Kind), Scope: "environment", State: string(goal.DesiredState), Ownership: "owned",
			})
		}
		sort.Slice(projected.PortLeases, func(left, right int) bool { return projected.PortLeases[left].ID < projected.PortLeases[right].ID })
		sort.Slice(projected.InfrastructureLeases, func(left, right int) bool {
			return projected.InfrastructureLeases[left].ID < projected.InfrastructureLeases[right].ID
		})
		return projected, nil
	}
}

func stableConfiguredEnvironmentID(profileKey, worktreeID string) string {
	digest := sha256.Sum256([]byte(profileKey + "\x00" + worktreeID))
	return "environment_" + base64.RawURLEncoding.EncodeToString(digest[:16])
}

func configuredExecutablePath(home string, profile configuration.Repository) string {
	directories := map[string]struct{}{
		"/opt/homebrew/bin": {}, "/usr/local/bin": {}, "/usr/bin": {}, "/bin": {}, "/usr/sbin": {}, "/sbin": {},
	}
	for _, step := range profile.Preparation.Steps {
		directories[filepath.Dir(step.Executable)] = struct{}{}
	}
	for _, service := range profile.Services {
		directories[filepath.Dir(service.Command.Executable)] = struct{}{}
		for _, command := range service.Prepare {
			directories[filepath.Dir(command.Executable)] = struct{}{}
		}
	}
	for _, action := range profile.Actions {
		if action.Command != nil {
			directories[filepath.Dir(action.Command.Executable)] = struct{}{}
		}
	}
	for _, toolchain := range profile.Toolchains {
		if toolchain.Executable != "" {
			directories[filepath.Dir(toolchain.Executable)] = struct{}{}
		}
	}
	values := make([]string, 0, len(directories))
	for directory := range directories {
		if filepath.IsAbs(directory) {
			values = append(values, directory)
		}
	}
	sort.Strings(values)
	_ = home
	return strings.Join(values, string(os.PathListSeparator))
}

func configuredFirstPort(inventory repositoryInventory) int {
	if inventory.FirstPort >= 1024 {
		return inventory.FirstPort
	}
	return defaultFirstDynamicPort
}

func configuredLastPort(inventory repositoryInventory) int {
	if inventory.LastPort >= configuredFirstPort(inventory) {
		return inventory.LastPort
	}
	return defaultLastDynamicPort
}
