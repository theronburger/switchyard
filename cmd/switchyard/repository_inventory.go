package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	"github.com/theronburger/switchyard/internal/configuration"
	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	controlconfig "github.com/theronburger/switchyard/internal/control/config"
	"github.com/theronburger/switchyard/internal/control/inventory"
	marketplacecontrol "github.com/theronburger/switchyard/internal/control/marketplace"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/state"
)

const repositoryRootOverride = "SWITCHYARD_REPOSITORY_ROOT"
const gitExecutableOverride = "SWITCHYARD_GIT_EXECUTABLE"
const marketplaceBaseReference = "origin/main"

type repositoryInventory struct {
	Repositories   []contractv1.Repository
	Alerts         []contractv1.Alert
	Configurations map[string]controlconfig.RepositoryConfiguration
	Profiles       map[string]configuration.Repository
	ProfileKeys    map[string]string
	ProfileDigests map[string]string
	Complete       bool
	AttemptedAt    time.Time
}

func restoreWorkspaceInventory(
	ctx context.Context,
	store *state.Store,
	inventory *repositoryInventory,
) error {
	journal, err := state.NewWorkspaceJournal(store)
	if err != nil {
		return err
	}
	results, err := journal.ListCurrent(ctx)
	if err != nil {
		return err
	}
	for _, result := range results {
		for repositoryIndex := range inventory.Repositories {
			for worktreeIndex := range inventory.Repositories[repositoryIndex].Worktrees {
				worktree := &inventory.Repositories[repositoryIndex].Worktrees[worktreeIndex]
				if worktree.ID != result.WorktreeID {
					continue
				}
				worktree.Workspace = contractWorkspaceStatus(result)
			}
		}
	}
	return nil
}

func contractWorkspaceStatus(result workspacecontrol.Result) *contractv1.WorkspaceStatus {
	toolchains := make([]contractv1.WorkspaceToolchain, len(result.Toolchains))
	for index, toolchain := range result.Toolchains {
		toolchains[index] = contractv1.WorkspaceToolchain{
			ID: toolchain.ID, RequestedVersion: toolchain.RequestedVersion,
			ResolvedVersion: toolchain.ResolvedVersion,
		}
	}
	sort.Slice(toolchains, func(left, right int) bool { return toolchains[left].ID < toolchains[right].ID })
	return &contractv1.WorkspaceStatus{
		Ownership: string(result.Ownership), State: string(result.State),
		Fingerprint: result.Fingerprint, PreparedAt: result.PreparedAt, Toolchains: toolchains,
	}
}

func discoverRepositoryInventory(ctx context.Context, observedAt time.Time) repositoryInventory {
	rootPath := os.Getenv(repositoryRootOverride)
	if rootPath == "" && buildChannel == "development" {
		return repositoryInventory{
			Repositories:   []contractv1.Repository{},
			Alerts:         []contractv1.Alert{},
			Configurations: map[string]controlconfig.RepositoryConfiguration{},
			Complete:       true,
			AttemptedAt:    observedAt.UTC(),
		}
	}
	if rootPath == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return inventoryFailure(observedAt, "REPOSITORY_ROOT_UNAVAILABLE", "The default repository root is unavailable.")
		}
		rootPath = filepath.Join(userHome, "Developer", "marketplace")
	}

	resolver, err := controlconfig.NewResolver(controlconfig.OSManifestSource{})
	if err != nil {
		return inventoryFailure(observedAt, "REPOSITORY_CONFIGURATION_UNAVAILABLE", "Repository configuration is unavailable.")
	}
	resolution := resolver.Resolve(ctx, []controlconfig.ExplicitRepositoryRoot{{
		RootPath:    rootPath,
		Adapter:     "marketplace",
		DisplayName: "marketplace",
	}})
	result := repositoryInventory{
		Repositories: []contractv1.Repository{}, Alerts: []contractv1.Alert{},
		Configurations: make(map[string]controlconfig.RepositoryConfiguration),
		AttemptedAt:    observedAt.UTC(),
	}
	for _, resolutionError := range resolution.Errors {
		result.Alerts = append(result.Alerts, newInventoryAlert(
			observedAt,
			resolutionError.Code,
			resolutionError.Message,
			"error",
		))
	}
	if len(resolution.Repositories) != 1 || resolution.Repositories[0].Adapter != "marketplace" {
		if len(result.Alerts) == 0 {
			result.Alerts = append(result.Alerts, newInventoryAlert(
				observedAt,
				"REPOSITORY_ADAPTER_UNSUPPORTED",
				"The configured repository adapter is not supported.",
				"error",
			))
		}
		return result
	}

	reader, err := marketplacecontrol.NewRepositoryReader(
		marketplacecontrol.OSCommandRunner{},
		configuredGitExecutable(),
	)
	if err != nil {
		result.Alerts = append(result.Alerts, newInventoryAlert(
			observedAt,
			"REPOSITORY_READER_UNAVAILABLE",
			"The Marketplace repository reader is unavailable.",
			"error",
		))
		return result
	}
	discovery, err := inventory.New(reader)
	if err != nil {
		result.Alerts = append(result.Alerts, newInventoryAlert(
			observedAt,
			"REPOSITORY_INVENTORY_UNAVAILABLE",
			"Repository inventory is unavailable.",
			"error",
		))
		return result
	}
	discovered := discovery.DiscoverRepository(ctx, resolution.Repositories[0].RootPath)
	if discovered.Repository != nil {
		repository := *discovered.Repository
		observationTime := observedAt.UTC()
		repository.Observation = &contractv1.RepositoryObservation{
			ObservedAt: &observationTime, LastAttemptAt: observationTime,
		}
		repository.DisplayName = resolution.Repositories[0].DisplayName
		runtimeCatalog, runtimeError := marketplaceRuntimeCatalog(resolution.Repositories[0].Runtime)
		if runtimeError != nil {
			result.Alerts = append(result.Alerts, newInventoryAlert(
				observedAt,
				"REPOSITORY_RUNTIME_CONFIGURATION_INVALID",
				"The configured Marketplace runtime catalog is invalid.",
				"error",
			))
			return result
		}
		repository.Runtime = &runtimeCatalog
		changeReader, changeReaderError := marketplacecontrol.NewGitChangeReader(
			marketplacecontrol.OSCommandRunner{},
			configuredGitExecutable(),
			marketplaceBaseReference,
		)
		if changeReaderError == nil {
			enrichMarketplaceWorktreeChanges(ctx, &repository, changeReader)
		}
		result.Repositories = append(result.Repositories, repository)
		result.Configurations[repository.ID] = resolution.Repositories[0]
		result.Complete = true
	}
	for _, alert := range discovered.Alerts {
		result.Alerts = append(result.Alerts, newInventoryAlert(
			observedAt,
			string(alert.Code),
			alert.Summary,
			alert.Severity,
		))
	}
	for _, discoveryError := range discovered.Errors {
		result.Alerts = append(result.Alerts, newInventoryAlert(
			observedAt,
			string(discoveryError.Code),
			discoveryError.Message,
			"error",
		))
	}
	result.Alerts = deduplicateInventoryAlerts(result.Alerts)
	sort.Slice(result.Alerts, func(left, right int) bool {
		return result.Alerts[left].ID < result.Alerts[right].ID
	})
	return result
}

type marketplaceWorktreeChangeResult struct {
	index   int
	changes marketplacecontrol.WorktreeChanges
	err     error
}

func enrichMarketplaceWorktreeChanges(
	ctx context.Context,
	repository *contractv1.Repository,
	reader marketplacecontrol.GitChangeReader,
) {
	const maximumConcurrentReaders = 4
	results := make(chan marketplaceWorktreeChangeResult, len(repository.Worktrees))
	semaphore := make(chan struct{}, maximumConcurrentReaders)
	var readers sync.WaitGroup
	for index, worktree := range repository.Worktrees {
		if worktree.Git.Prunable {
			continue
		}
		readers.Add(1)
		go func(index int, path string) {
			defer readers.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- marketplaceWorktreeChangeResult{index: index, err: ctx.Err()}
				return
			}
			changes, err := reader.Read(ctx, path)
			results <- marketplaceWorktreeChangeResult{index: index, changes: changes, err: err}
		}(index, worktree.Path)
	}
	readers.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			continue
		}
		worktree := &repository.Worktrees[result.index]
		worktree.Git.HasTrackedChanges = result.changes.HasTrackedChanges
		worktree.Git.HasUntrackedFiles = result.changes.HasUntrackedFiles
		worktree.Git.HasUnpushedCommits = result.changes.HasUnpushedCommits
		worktree.Changes = contractWorktreeChanges(result.changes)
	}
}

func contractWorktreeChanges(changes marketplacecontrol.WorktreeChanges) *contractv1.WorktreeChanges {
	services := make([]contractv1.ServiceLineChanges, 0, len(changes.Services))
	for _, service := range changes.Services {
		services = append(services, contractv1.ServiceLineChanges{
			ServiceID:   service.ServiceID,
			Committed:   contractLineChanges(service.Committed),
			Uncommitted: contractLineChanges(service.Uncommitted),
		})
	}
	return &contractv1.WorktreeChanges{
		BaseRevision:      changes.BaseRevision,
		Committed:         contractLineChanges(changes.Committed),
		Uncommitted:       contractLineChanges(changes.Uncommitted),
		SharedCommitted:   contractLineChanges(changes.SharedCommitted),
		SharedUncommitted: contractLineChanges(changes.SharedUncommitted),
		Services:          services,
	}
}

func contractLineChanges(changes marketplacecontrol.LineChanges) contractv1.LineChanges {
	return contractv1.LineChanges{
		Additions: changes.Additions,
		Deletions: changes.Deletions,
		Files:     changes.Files,
	}
}

func marketplaceRuntimeCatalog(settings controlconfig.RuntimeSettings) (contractv1.RepositoryRuntime, error) {
	knownTargets := make(map[string]marketplaceadapter.RuntimeTarget)
	for _, target := range marketplaceadapter.KnownRuntimeTargets() {
		knownTargets[target.ID] = target
	}
	knownServices := make(map[string]marketplaceadapter.RuntimeService)
	for _, service := range marketplaceadapter.KnownRuntimeServices() {
		knownServices[service.ID] = service
	}

	targetIDs := append([]string(nil), settings.Targets...)
	serviceIDs := append([]string(nil), settings.Services...)
	defaultTargetID := settings.DefaultTarget
	if defaultTargetID == "" {
		defaultTargetID = marketplaceadapter.DefaultRuntimeTargetID()
		for _, target := range marketplaceadapter.KnownRuntimeTargets() {
			targetIDs = append(targetIDs, target.ID)
		}
		for _, service := range marketplaceadapter.KnownRuntimeServices() {
			serviceIDs = append(serviceIDs, service.ID)
		}
	}

	runtime := contractv1.RepositoryRuntime{
		DefaultTargetID: defaultTargetID,
		Targets:         make([]contractv1.RuntimeTarget, 0, len(targetIDs)),
		Services:        make([]contractv1.RuntimeService, 0, len(serviceIDs)),
	}
	warnOnStartTargets := make(map[string]struct{}, len(settings.WarnOnStartTargets))
	for _, targetID := range settings.WarnOnStartTargets {
		warnOnStartTargets[targetID] = struct{}{}
	}
	for _, targetID := range targetIDs {
		target, found := knownTargets[targetID]
		if !found {
			return contractv1.RepositoryRuntime{}, errors.New("unknown Marketplace runtime target")
		}
		warnOnStart := target.WarnOnStart
		if settings.WarnOnStartTargets != nil {
			_, warnOnStart = warnOnStartTargets[targetID]
		}
		runtime.Targets = append(runtime.Targets, contractv1.RuntimeTarget{
			ID: target.ID, DisplayName: target.DisplayName, Risk: target.Risk, WarnOnStart: warnOnStart,
		})
	}
	catalog := marketplaceadapter.DefaultCatalog()
	for _, serviceID := range serviceIDs {
		service, found := knownServices[serviceID]
		if !found {
			return contractv1.RepositoryRuntime{}, errors.New("unknown Marketplace runtime service")
		}
		_, available := catalog.Definition(serviceID)
		unavailableReason := ""
		if !available {
			unavailableReason = "Switchyard does not yet have a complete isolated launch plan for this Marketplace service."
		}
		runtime.Services = append(runtime.Services, contractv1.RuntimeService{
			ID: service.ID, DisplayName: service.DisplayName, Kind: string(service.Kind),
			Available: available, UnavailableReason: unavailableReason,
		})
	}
	if err := (contractv1.StatusSnapshot{
		SchemaVersion: contractv1.SchemaVersion,
		Daemon:        contractv1.DaemonStatus{InstanceID: "validation"},
		Repositories: []contractv1.Repository{{
			ID: "validation", Worktrees: []contractv1.Worktree{}, Runtime: &runtime,
		}},
		Environments: []contractv1.Environment{}, Operations: []contractv1.Operation{}, Alerts: []contractv1.Alert{},
	}).Validate(); err != nil {
		return contractv1.RepositoryRuntime{}, errors.New("invalid Marketplace runtime catalog")
	}
	return runtime, nil
}

func deduplicateInventoryAlerts(alerts []contractv1.Alert) []contractv1.Alert {
	byID := make(map[string]contractv1.Alert, len(alerts))
	for _, alert := range alerts {
		previous, exists := byID[alert.ID]
		if !exists || (previous.Severity != "error" && alert.Severity == "error") {
			byID[alert.ID] = alert
		}
	}
	unique := make([]contractv1.Alert, 0, len(byID))
	for _, alert := range byID {
		unique = append(unique, alert)
	}
	return unique
}

func configuredGitExecutable() string {
	if configured := os.Getenv(gitExecutableOverride); configured != "" {
		return configured
	}
	commandLineToolsGit := "/Library/Developer/CommandLineTools/usr/bin/git"
	if info, err := os.Stat(commandLineToolsGit); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
		return commandLineToolsGit
	}
	return "/usr/bin/git"
}

func inventoryFailure(observedAt time.Time, code, summary string) repositoryInventory {
	return repositoryInventory{
		Repositories: []contractv1.Repository{},
		Alerts:       []contractv1.Alert{newInventoryAlert(observedAt, code, summary, "error")},
		AttemptedAt:  observedAt.UTC(),
	}
}

func newInventoryAlert(observedAt time.Time, code, summary, severity string) contractv1.Alert {
	digest := sha256.Sum256([]byte(code))
	return contractv1.Alert{
		ID:          "alert_inventory_" + base64.RawURLEncoding.EncodeToString(digest[:12]),
		Severity:    severity,
		Code:        code,
		Summary:     summary,
		Status:      "active",
		FirstSeenAt: observedAt.UTC(),
		LastSeenAt:  observedAt.UTC(),
		Occurrences: 1,
	}
}

func mergeRepositoryInventory(
	snapshot contractv1.StatusSnapshot,
	discovered repositoryInventory,
) contractv1.StatusSnapshot {
	if discovered.Complete {
		repositories := append([]contractv1.Repository{}, discovered.Repositories...)
		preservePullRequestObservations(repositories, snapshot.Repositories)
		knownRepositories := make(map[string]int, len(repositories))
		for index, repository := range repositories {
			knownRepositories[repository.ID] = index
		}
		for _, environment := range snapshot.Environments {
			repositoryIndex, repositoryFound := knownRepositories[environment.RepositoryID]
			if !repositoryFound {
				for _, previous := range snapshot.Repositories {
					if previous.ID != environment.RepositoryID {
						continue
					}
					repositories = append(repositories, previous)
					repositoryIndex = len(repositories) - 1
					knownRepositories[previous.ID] = repositoryIndex
					repositoryFound = true
					break
				}
			}
			if !repositoryFound || repositoryContainsWorktree(repositories[repositoryIndex], environment.WorktreeID) {
				continue
			}
			if previous, found := findPreviousWorktree(
				snapshot.Repositories, environment.RepositoryID, environment.WorktreeID,
			); found {
				repositories[repositoryIndex].Worktrees = append(repositories[repositoryIndex].Worktrees, previous)
			}
		}
		for repositoryIndex := range repositories {
			sort.Slice(repositories[repositoryIndex].Worktrees, func(left, right int) bool {
				return repositories[repositoryIndex].Worktrees[left].ID < repositories[repositoryIndex].Worktrees[right].ID
			})
		}
		sort.Slice(repositories, func(left, right int) bool {
			return repositories[left].ID < repositories[right].ID
		})
		snapshot.Repositories = repositories
	} else {
		markRepositoryObservationsStale(snapshot.Repositories, discovered)
		if snapshot.Repositories == nil {
			snapshot.Repositories = []contractv1.Repository{}
		}
	}

	alerts := make([]contractv1.Alert, 0, len(snapshot.Alerts)+len(discovered.Alerts))
	for _, alert := range snapshot.Alerts {
		if !strings.HasPrefix(alert.ID, "alert_inventory_") {
			alerts = append(alerts, alert)
		}
	}
	alerts = append(alerts, discovered.Alerts...)
	sort.Slice(alerts, func(left, right int) bool { return alerts[left].ID < alerts[right].ID })
	snapshot.Alerts = alerts
	return snapshot
}

func repositoryContainsWorktree(repository contractv1.Repository, worktreeID string) bool {
	for _, worktree := range repository.Worktrees {
		if worktree.ID == worktreeID {
			return true
		}
	}
	return false
}

func findPreviousWorktree(
	repositories []contractv1.Repository,
	repositoryID string,
	worktreeID string,
) (contractv1.Worktree, bool) {
	for _, repository := range repositories {
		if repository.ID != repositoryID {
			continue
		}
		for _, worktree := range repository.Worktrees {
			if worktree.ID == worktreeID {
				return worktree, true
			}
		}
	}
	return contractv1.Worktree{}, false
}

func markRepositoryObservationsStale(
	repositories []contractv1.Repository,
	discovered repositoryInventory,
) {
	attemptedAt := discovered.AttemptedAt.UTC()
	if attemptedAt.IsZero() {
		attemptedAt = time.Now().UTC()
	}
	errorCode := "REPOSITORY_OBSERVATION_FAILED"
	for _, alert := range discovered.Alerts {
		if alert.Severity == "error" && alert.Code != "" {
			errorCode = alert.Code
			break
		}
	}
	for repositoryIndex := range repositories {
		previousObservedAt := (*time.Time)(nil)
		if repositories[repositoryIndex].Observation != nil {
			previousObservedAt = repositories[repositoryIndex].Observation.ObservedAt
		}
		repositories[repositoryIndex].Observation = &contractv1.RepositoryObservation{
			ObservedAt: previousObservedAt, LastAttemptAt: attemptedAt,
			Stale: true, ErrorCode: errorCode,
		}
	}
}

func preservePullRequestObservations(
	discovered []contractv1.Repository,
	previous []contractv1.Repository,
) {
	type worktreeIdentity struct {
		repositoryID string
		worktreeID   string
		branch       string
		headRevision string
	}
	observations := make(map[worktreeIdentity]*contractv1.PullRequestObservation)
	for _, repository := range previous {
		for _, worktree := range repository.Worktrees {
			if worktree.PullRequest != nil {
				observations[worktreeIdentity{
					repositoryID: repository.ID, worktreeID: worktree.ID,
					branch: worktree.Branch, headRevision: worktree.HeadRevision,
				}] = worktree.PullRequest
			}
		}
	}
	for repositoryIndex := range discovered {
		repository := &discovered[repositoryIndex]
		for worktreeIndex := range repository.Worktrees {
			worktree := &repository.Worktrees[worktreeIndex]
			worktree.PullRequest = observations[worktreeIdentity{
				repositoryID: repository.ID, worktreeID: worktree.ID,
				branch: worktree.Branch, headRevision: worktree.HeadRevision,
			}]
		}
	}
}
