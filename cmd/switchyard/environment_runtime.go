package main

import (
	"bufio"
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

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	marketplacecontrol "github.com/theronburger/switchyard/internal/control/marketplace"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/health"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
	"github.com/theronburger/switchyard/internal/state"
)

const (
	nodeExecutableOverride   = "SWITCHYARD_NODE_EXECUTABLE"
	dockerExecutableOverride = "SWITCHYARD_DOCKER_EXECUTABLE"
	defaultFirstDynamicPort  = 30000
	defaultLastDynamicPort   = 49999
	maximumLocalConfigBytes  = 1024 * 1024
)

type environmentRuntime struct {
	actions      *daemon.EnvironmentActionService
	observerDone <-chan error
}

type marketplaceEnvironment struct {
	EnvironmentID string
	RepositoryID  string
	Worktree      contractv1.Worktree
	PortDefaults  map[string]int
}

type marketplaceActionResolver struct {
	byWorktree    map[string]marketplaceEnvironment
	byEnvironment map[string]marketplaceEnvironment
	catalog       marketplaceadapter.Catalog
}

func buildEnvironmentRuntime(
	ctx context.Context,
	store *state.Store,
	paths applicationPaths,
	instanceID string,
	discovered repositoryInventory,
) (*environmentRuntime, error) {
	environments, registrations, err := marketplaceRegistrations(paths, instanceID, discovered.Repositories)
	if err != nil {
		return nil, err
	}
	projector := marketplaceEnvironmentProjector(environments, marketplaceadapter.DefaultCatalog())
	journal, err := state.NewEnvironmentJournal(store, projector)
	if err != nil {
		return nil, err
	}
	currentPage, err := journal.ListCurrent(ctx, "", 1)
	if err != nil {
		return nil, err
	}
	incomplete, err := journal.Incomplete(ctx)
	if err != nil {
		return nil, err
	}
	if len(registrations) == 0 {
		if len(currentPage.Results) != 0 || currentPage.HasMore || len(incomplete) != 0 {
			return nil, errors.New("persisted environments cannot be recovered without their worktrees")
		}
		return &environmentRuntime{}, nil
	}

	registry, err := marketplacecontrol.NewEnvironmentRegistry(registrations)
	if err != nil {
		return nil, err
	}
	planner, err := marketplacecontrol.NewDefaultPlanBuilder(registry)
	if err != nil {
		return nil, err
	}
	projections, err := marketplacecontrol.NewProjectionApplier(registry)
	if err != nil {
		return nil, err
	}
	prober, err := health.NewProber(health.ProberConfig{})
	if err != nil {
		return nil, err
	}
	readiness, err := marketplacecontrol.NewReadinessChecker(prober, marketplacecontrol.ReadinessConfig{})
	if err != nil {
		return nil, err
	}
	ports, err := portlease.NewAllocator(portlease.Config{
		Host: "127.0.0.1", FirstPort: defaultFirstDynamicPort, LastPort: defaultLastDynamicPort,
	})
	if err != nil {
		return nil, err
	}
	if err := restoreEnvironmentLeases(ctx, journal, ports); err != nil {
		return nil, err
	}

	dockerBinary, err := configuredDockerExecutable()
	if err != nil {
		return nil, err
	}
	dockerRunner := containerhost.OSRunner{}
	dockerInventory := containerhost.DockerInventory{Runner: dockerRunner, DockerBinary: dockerBinary}
	coordinator, err := environmentcontrol.NewCoordinator(environmentcontrol.Config{
		Journal: journal, Ports: ports, Planner: planner, Projections: projections,
		Preparations: marketplacecontrol.OSPreparationRunner{},
		Infrastructure: environmentcontrol.ContainerInfrastructureHost{
			Resources: dockerInventory,
			Planner:   containerhost.Planner{DockerBinary: dockerBinary},
			Applier: containerhost.Reconciler{
				Runner: dockerRunner, Resources: dockerInventory, DockerBinary: dockerBinary,
			},
		},
		Processes: processhost.New(processhost.Config{}), Readiness: readiness,
		RollbackTimeout: 45 * time.Second,
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
	observer, err := environmentcontrol.NewLiveObserver(environmentcontrol.LiveObserverConfig{
		Coordinator: coordinator,
	})
	if err != nil {
		return nil, err
	}
	initialObservation, cancelInitialObservation := context.WithTimeout(
		ctx, environmentcontrol.DefaultLiveObservationTimeout,
	)
	err = observer.RefreshOnce(initialObservation)
	cancelInitialObservation()
	if err != nil {
		return nil, err
	}
	observerDone := make(chan error, 1)
	resolver := newMarketplaceActionResolver(environments, marketplaceadapter.DefaultCatalog())
	actions, err := daemon.NewEnvironmentActionService(daemon.EnvironmentActionServiceConfig{
		Lifecycle: ctx, Store: store, Journal: journal, Coordinator: coordinator, Resolver: resolver,
	})
	if err != nil {
		return nil, err
	}
	go func() {
		observerDone <- observer.Run(ctx)
	}()
	return &environmentRuntime{actions: actions, observerDone: observerDone}, nil
}

func (runtime *environmentRuntime) CloseAndWait(ctx context.Context) error {
	var actionError error
	if runtime.actions != nil {
		actionError = runtime.actions.CloseAndWait(ctx)
	}
	var observerError error
	if runtime.observerDone != nil {
		select {
		case observerError = <-runtime.observerDone:
			if errors.Is(observerError, context.Canceled) {
				observerError = nil
			}
		case <-ctx.Done():
			observerError = ctx.Err()
		}
	}
	return errors.Join(actionError, observerError)
}

func restoreEnvironmentLeases(
	ctx context.Context,
	journal *state.EnvironmentJournal,
	allocator *portlease.Allocator,
) error {
	after := ""
	for {
		page, err := journal.ListCurrent(ctx, after, state.MaximumCurrentEnvironmentPageSize)
		if err != nil {
			return err
		}
		for _, result := range page.Results {
			if err := allocator.Restore(result.Ports); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		if page.NextEnvironmentID == "" || page.NextEnvironmentID == after {
			return errors.New("environment lease restoration cursor did not advance")
		}
		after = page.NextEnvironmentID
	}
}

func marketplaceRegistrations(
	paths applicationPaths,
	instanceID string,
	repositories []contractv1.Repository,
) ([]marketplaceEnvironment, []marketplacecontrol.EnvironmentRegistration, error) {
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	temporaryDirectory := filepath.Clean(os.TempDir())
	environments := make([]marketplaceEnvironment, 0)
	registrations := make([]marketplacecontrol.EnvironmentRegistration, 0)
	for _, repository := range repositories {
		if repository.Adapter != "marketplace" {
			continue
		}
		for _, worktree := range repository.Worktrees {
			if worktree.Git.Prunable {
				continue
			}
			nodeExecutable, err := resolveNodeExecutable(worktree.Path)
			if err != nil {
				return nil, nil, err
			}
			yarnCJS, err := resolveYarnCJS(worktree.Path)
			if err != nil {
				return nil, nil, err
			}
			portDefaults, err := readMarketplacePortDefaults(worktree.Path)
			if err != nil {
				return nil, nil, err
			}
			environmentID := stableEnvironmentID(worktree.ID)
			environments = append(environments, marketplaceEnvironment{
				EnvironmentID: environmentID, RepositoryID: repository.ID,
				Worktree: worktree, PortDefaults: portDefaults,
			})
			registrations = append(registrations, marketplacecontrol.EnvironmentRegistration{
				EnvironmentID: environmentID, WorktreeRoot: worktree.Path,
				NodeExecutable: nodeExecutable, YarnCJS: yarnCJS,
				RunRoot:       filepath.Join(paths.directory, "runtime"),
				HomeDirectory: homeDirectory, TemporaryDirectory: temporaryDirectory,
				ExecutablePath: filepath.Join(filepath.Dir(nodeExecutable)) +
					":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
				DaemonInstanceID: instanceID,
			})
		}
	}
	return environments, registrations, nil
}

func newMarketplaceActionResolver(
	environments []marketplaceEnvironment,
	catalog marketplaceadapter.Catalog,
) marketplaceActionResolver {
	resolver := marketplaceActionResolver{
		byWorktree:    make(map[string]marketplaceEnvironment, len(environments)),
		byEnvironment: make(map[string]marketplaceEnvironment, len(environments)),
		catalog:       catalog,
	}
	for _, environment := range environments {
		resolver.byWorktree[environment.Worktree.ID] = environment
		resolver.byEnvironment[environment.EnvironmentID] = environment
	}
	return resolver
}

func (resolver marketplaceActionResolver) ResolveStart(
	_ context.Context,
	request contractv1.StartEnvironmentRequest,
) (daemon.EnvironmentStartResolution, error) {
	registered, found := resolver.byWorktree[request.WorktreeID]
	if !found {
		return daemon.EnvironmentStartResolution{}, &daemon.ActionError{
			Status: 404, Contract: contractv1.ContractError{
				Code: "WORKTREE_NOT_FOUND", Message: "The requested worktree is not available.",
			},
		}
	}
	serviceIDs := append([]string(nil), request.ServiceIDs...)
	sort.Strings(serviceIDs)
	reservations := make([]portlease.Reservation, 0)
	for _, serviceID := range serviceIDs {
		definition, found := resolver.catalog.Definition(serviceID)
		if !found {
			return daemon.EnvironmentStartResolution{}, &daemon.ActionError{
				Status: 400, Contract: contractv1.ContractError{
					Code: "SERVICE_NOT_SUPPORTED", Message: "A requested service is not supported for this repository.",
				},
			}
		}
		preferredByRequirement := make(map[string]int, len(definition.PortRequirements))
		for _, requirement := range definition.PortRequirements {
			preferred := requirement.PreferredPort
			if configured := registered.PortDefaults[requirement.PreferredPortEnvironment]; configured != 0 {
				preferred = configured
			}
			if requirement.PreferredRelativeTo != "" {
				base := preferredByRequirement[requirement.PreferredRelativeTo]
				if base > 0 && base+requirement.PreferredOffset <= 65535 {
					preferred = base + requirement.PreferredOffset
				}
			}
			preferredByRequirement[requirement.ID] = preferred
			reservation := portlease.Reservation{Key: portlease.Key{
				EnvironmentID: registered.EnvironmentID, ServiceID: serviceID, Purpose: requirement.Purpose,
			}}
			if preferred != 0 {
				reservation.PreferredPorts = []int{preferred}
			}
			reservations = append(reservations, reservation)
		}
	}
	return daemon.EnvironmentStartResolution{
		EnvironmentID: registered.EnvironmentID, Ports: reservations,
		Intent: environmentcontrol.PlanIntent{Adapter: "marketplace", ServiceIDs: serviceIDs},
	}, nil
}

func (resolver marketplaceActionResolver) ResolveStop(
	_ context.Context,
	environmentID string,
	_ contractv1.StopEnvironmentRequest,
) error {
	if _, found := resolver.byEnvironment[environmentID]; !found {
		return &daemon.ActionError{Status: 404, Contract: contractv1.ContractError{
			Code: "ENVIRONMENT_NOT_FOUND", Message: "The requested environment is not available.",
		}}
	}
	return nil
}

func marketplaceEnvironmentProjector(
	environments []marketplaceEnvironment,
	catalog marketplaceadapter.Catalog,
) state.EnvironmentProjector {
	byID := make(map[string]marketplaceEnvironment, len(environments))
	for _, environment := range environments {
		byID[environment.EnvironmentID] = environment
	}
	return func(
		current *contractv1.Environment,
		result environmentcontrol.EnvironmentResult,
	) (contractv1.Environment, error) {
		metadata, found := byID[result.EnvironmentID]
		if !found {
			return contractv1.Environment{}, errors.New("environment metadata is unavailable")
		}
		projected := contractv1.Environment{
			ID: result.EnvironmentID, RepositoryID: metadata.RepositoryID, WorktreeID: metadata.Worktree.ID,
			DisplayName:  environmentDisplayName(metadata.Worktree),
			DesiredState: string(result.State), ObservedState: string(result.State),
			Health: "unknown", Services: []contractv1.Service{}, PortLeases: []contractv1.PortLease{},
			InfrastructureLeases: []contractv1.InfrastructureLease{}, URLs: map[string]string{},
			AttentionAlertIDs: []string{},
		}
		if current != nil {
			projected.AttentionAlertIDs = append([]string(nil), current.AttentionAlertIDs...)
		}
		leaseIDsByService := make(map[string][]string)
		for _, lease := range result.Ports {
			leaseID := stablePortLeaseID(lease.Key)
			acquiredAt := result.UpdatedAt
			if current != nil {
				for _, previous := range current.PortLeases {
					if previous.ID == leaseID {
						acquiredAt = previous.AcquiredAt
						break
					}
				}
			}
			projected.PortLeases = append(projected.PortLeases, contractv1.PortLease{
				ID: leaseID, ServiceID: lease.Key.ServiceID, Purpose: lease.Key.Purpose,
				Host: lease.Host, Port: lease.Port, State: "leased", AcquiredAt: acquiredAt,
			})
			leaseIDsByService[lease.Key.ServiceID] = append(leaseIDsByService[lease.Key.ServiceID], leaseID)
			if lease.Key.Purpose == "http" {
				projected.URLs[lease.Key.ServiceID] = "http://" + lease.Host + ":" + strconv.Itoa(lease.Port)
			}
		}
		serviceResults := make(map[string]environmentcontrol.ServiceResult, len(result.Services))
		serviceIDs := make(map[string]struct{})
		for _, service := range result.Services {
			serviceResults[service.ID] = service
			serviceIDs[service.ID] = struct{}{}
		}
		for serviceID := range leaseIDsByService {
			serviceIDs[serviceID] = struct{}{}
		}
		if len(serviceIDs) == 0 && current != nil {
			for _, service := range current.Services {
				serviceIDs[service.ID] = struct{}{}
			}
		}
		orderedServiceIDs := make([]string, 0, len(serviceIDs))
		for serviceID := range serviceIDs {
			orderedServiceIDs = append(orderedServiceIDs, serviceID)
		}
		sort.Strings(orderedServiceIDs)
		allHealthy := len(orderedServiceIDs) > 0 && result.State == domain.EnvironmentRunning
		anyUnhealthy := false
		anyProcessDegraded := false
		for _, serviceID := range orderedServiceIDs {
			definition, known := catalog.Definition(serviceID)
			if !known {
				return contractv1.Environment{}, errors.New("environment service metadata is unavailable")
			}
			serviceResult, running := serviceResults[serviceID]
			service := contractv1.Service{
				ID: serviceID, DisplayName: definition.DisplayName, Kind: string(definition.Kind),
				DesiredState: string(result.State), ObservedState: string(result.State), Health: "unknown",
				PortLeaseIDs: append([]string(nil), leaseIDsByService[serviceID]...),
			}
			if running {
				if serviceResult.Observation.State != "" {
					service.ObservedState = serviceResult.Observation.State
				}
				service.Health = serviceResult.Health.Health
				if service.Health == "" {
					service.Health = "unknown"
				}
				processCount := len(serviceResult.Process.Members)
				if !serviceResult.Observation.ObservedAt.IsZero() {
					processCount = serviceResult.Observation.ProcessCount
				}
				service.Run = &contractv1.ServiceRun{
					ID: result.RunID, StartedAt: serviceResult.Process.StartedAt,
					ProcessCount: processCount, CPUPercent: serviceResult.Observation.CPUPercent,
					MemoryBytes: serviceResult.Observation.MemoryBytes,
				}
				projected.Resources.MemoryBytes = saturatingResourceAdd(
					projected.Resources.MemoryBytes, serviceResult.Observation.MemoryBytes,
				)
				projected.Resources.CPUPercent += serviceResult.Observation.CPUPercent
				if service.ObservedState != string(domain.EnvironmentRunning) || serviceResult.Observation.Code != "" {
					anyProcessDegraded = true
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
		if result.State == domain.EnvironmentRunning && anyProcessDegraded {
			projected.ObservedState = "degraded"
		}
		if allHealthy {
			projected.Health = "healthy"
		} else if anyUnhealthy {
			projected.Health = "unhealthy"
		} else if result.State == domain.EnvironmentRunning {
			projected.Health = "degraded"
		}
		for _, goal := range result.Infrastructure {
			projected.InfrastructureLeases = append(projected.InfrastructureLeases, contractv1.InfrastructureLease{
				ID: stableInfrastructureLeaseID(goal.Identity), ServiceID: infrastructureServiceID(goal.Identity.ServiceID),
				DisplayName: goal.Name, Kind: string(goal.Kind), Scope: "environment",
				State: string(goal.DesiredState), Ownership: "owned",
			})
		}
		sort.Slice(projected.PortLeases, func(left, right int) bool {
			return projected.PortLeases[left].ID < projected.PortLeases[right].ID
		})
		sort.Slice(projected.InfrastructureLeases, func(left, right int) bool {
			return projected.InfrastructureLeases[left].ID < projected.InfrastructureLeases[right].ID
		})
		return projected, nil
	}
}

func saturatingResourceAdd(total, value int64) int64 {
	if value <= 0 {
		return total
	}
	if total > int64(^uint64(0)>>1)-value {
		return int64(^uint64(0) >> 1)
	}
	return total + value
}

func stableEnvironmentID(worktreeID string) string {
	digest := sha256.Sum256([]byte("marketplace\x00" + worktreeID))
	return "environment_" + base64.RawURLEncoding.EncodeToString(digest[:16])
}

func stablePortLeaseID(key portlease.Key) string {
	digest := sha256.Sum256([]byte(key.EnvironmentID + "\x00" + key.ServiceID + "\x00" + key.Purpose))
	return "port_" + base64.RawURLEncoding.EncodeToString(digest[:12])
}

func stableInfrastructureLeaseID(identity containerhost.Identity) string {
	digest := sha256.Sum256([]byte(identity.EnvironmentID + "\x00" + identity.ServiceID + "\x00" + identity.RunID + "\x00" + identity.InstanceID))
	return "infra_" + base64.RawURLEncoding.EncodeToString(digest[:12])
}

func infrastructureServiceID(identityServiceID string) string {
	serviceID, _, _ := strings.Cut(identityServiceID, ".")
	return serviceID
}

func environmentDisplayName(worktree contractv1.Worktree) string {
	if worktree.Branch != "" {
		return worktree.Branch
	}
	return filepath.Base(worktree.Path)
}

func resolveNodeExecutable(worktreeRoot string) (string, error) {
	if configured := os.Getenv(nodeExecutableOverride); configured != "" {
		return requireExecutable(configured)
	}
	contents, err := readBoundedRegularFile(filepath.Join(worktreeRoot, ".nvmrc"), 128)
	if err != nil {
		return "", err
	}
	requested := strings.TrimPrefix(strings.TrimSpace(string(contents)), "v")
	requestedParts, ok := numericVersionParts(requested)
	if !ok {
		return "", errors.New("Marketplace .nvmrc is invalid")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	versionsRoot := filepath.Join(home, ".nvm", "versions", "node")
	entries, err := os.ReadDir(versionsRoot)
	if err != nil || len(entries) > 256 {
		return "", errors.New("Marketplace Node runtime is unavailable")
	}
	type candidate struct {
		parts []int
		path  string
	}
	candidates := make([]candidate, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		parts, valid := numericVersionParts(strings.TrimPrefix(entry.Name(), "v"))
		if !valid || !versionHasPrefix(parts, requestedParts) {
			continue
		}
		path := filepath.Join(versionsRoot, entry.Name(), "bin", "node")
		if executable, executableErr := requireExecutable(path); executableErr == nil {
			candidates = append(candidates, candidate{parts: parts, path: executable})
		}
	}
	if len(candidates) == 0 {
		return "", errors.New("Marketplace Node runtime is unavailable")
	}
	sort.Slice(candidates, func(left, right int) bool {
		return compareVersionParts(candidates[left].parts, candidates[right].parts) > 0
	})
	return candidates[0].path, nil
}

func resolveYarnCJS(worktreeRoot string) (string, error) {
	contents, err := readBoundedRegularFile(filepath.Join(worktreeRoot, ".yarnrc.yml"), 64*1024)
	if err != nil {
		return "", err
	}
	var yarnPath string
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "yarnPath:") {
			continue
		}
		if yarnPath != "" {
			return "", errors.New("Marketplace Yarn path is ambiguous")
		}
		yarnPath = strings.TrimSpace(strings.TrimPrefix(line, "yarnPath:"))
		yarnPath = strings.Trim(yarnPath, "\"'")
	}
	if scanner.Err() != nil || yarnPath == "" || filepath.IsAbs(yarnPath) ||
		filepath.Clean(yarnPath) != yarnPath || strings.HasPrefix(yarnPath, "..") ||
		filepath.Ext(yarnPath) != ".cjs" {
		return "", errors.New("Marketplace Yarn path is invalid")
	}
	path := filepath.Join(worktreeRoot, yarnPath)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Marketplace Yarn runtime is unavailable")
	}
	return path, nil
}

func readMarketplacePortDefaults(worktreeRoot string) (map[string]int, error) {
	defaults := make(map[string]int)
	for _, name := range []string{".env", ".env.development.local"} {
		contents, err := readBoundedRegularFile(filepath.Join(worktreeRoot, name), maximumLocalConfigBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		parsed, err := marketplaceadapter.DefaultCatalog().ParsePortDefaults(contents)
		if err != nil {
			return nil, errors.New("Marketplace local port defaults are invalid")
		}
		for _, portDefault := range parsed {
			defaults[portDefault.EnvironmentVariable] = portDefault.Port
		}
	}
	return defaults, nil
}

func configuredDockerExecutable() (string, error) {
	if configured := os.Getenv(dockerExecutableOverride); configured != "" {
		return requireExecutable(configured)
	}
	return requireExecutable("/opt/homebrew/bin/docker")
}

func requireExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("runtime executable path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved {
		return "", errors.New("runtime executable is unavailable")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		return "", errors.New("runtime executable is unavailable")
	}
	return resolved, nil
}

func readBoundedRegularFile(path string, maximumBytes int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maximumBytes {
		return nil, errors.New("local configuration file is unsafe")
	}
	contents, err := os.ReadFile(path)
	if err != nil || int64(len(contents)) > maximumBytes {
		return nil, errors.New("local configuration file is unavailable")
	}
	return contents, nil
}

func numericVersionParts(version string) ([]int, bool) {
	pieces := strings.Split(version, ".")
	if len(pieces) == 0 || len(pieces) > 4 {
		return nil, false
	}
	parts := make([]int, len(pieces))
	for index, piece := range pieces {
		value, err := strconv.Atoi(piece)
		if err != nil || value < 0 || value > 100000 {
			return nil, false
		}
		parts[index] = value
	}
	return parts, true
}

func versionHasPrefix(version, prefix []int) bool {
	if len(version) < len(prefix) {
		return false
	}
	for index := range prefix {
		if version[index] != prefix[index] {
			return false
		}
	}
	return true
}

func compareVersionParts(left, right []int) int {
	length := len(left)
	if len(right) > length {
		length = len(right)
	}
	for index := 0; index < length; index++ {
		leftValue, rightValue := 0, 0
		if index < len(left) {
			leftValue = left[index]
		}
		if index < len(right) {
			rightValue = right[index]
		}
		if leftValue < rightValue {
			return -1
		}
		if leftValue > rightValue {
			return 1
		}
	}
	return 0
}
