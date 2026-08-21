package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/control/inventory"
	"github.com/theronburger/switchyard/internal/control/repository"
	"github.com/theronburger/switchyard/internal/state"
)

func discoverAcceptedRepositoryInventory(
	ctx context.Context,
	store *state.Store,
	observedAt time.Time,
) (repositoryInventory, error) {
	accepted, err := store.ReadAcceptedConfiguration(ctx)
	if errors.Is(err, state.ErrConfigurationNotAccepted) {
		return repositoryInventory{
			Repositories: []contractv2.Repository{}, Alerts: []contractv2.Alert{},
			Profiles: map[string]configuration.Repository{}, ProfileKeys: map[string]string{},
			ProfileDigests: map[string]string{},
			Complete:       true, AttemptedAt: observedAt.UTC(),
		}, nil
	}
	if err != nil {
		return repositoryInventory{}, err
	}
	var document configuration.Document
	decoder := json.NewDecoder(bytes.NewReader(accepted.CanonicalPayload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return repositoryInventory{}, err
	}
	discovered := discoverConfiguredRepositoryInventory(
		ctx, observedAt, document, configuredGitExecutable(),
	)
	discovered.FirstPort = document.Machine.Ports.First
	discovered.LastPort = document.Machine.Ports.Last
	discovered.InheritedEnvironment = append([]string(nil), document.Machine.Execution.InheritedEnvironment...)
	discovered.AcceptedConfigurationDigest = accepted.Digest
	for repositoryID, key := range discovered.ProfileKeys {
		discovered.ProfileDigests[repositoryID] = accepted.RepositoryDigests[key]
	}
	return discovered, nil
}

const maximumConcurrentRepositoryDiscoveries = 4

type configuredRepositoryResult struct {
	key       string
	profile   configuration.Repository
	discovery inventory.DiscoveryResult
}

func discoverConfiguredRepositoryInventory(
	ctx context.Context,
	observedAt time.Time,
	document configuration.Document,
	gitExecutable string,
) repositoryInventory {
	return discoverConfiguredRepositories(ctx, observedAt, document, func(
		ctx context.Context,
		key string,
		profile configuration.Repository,
	) inventory.DiscoveryResult {
		reader := repository.GitReader{
			GitExecutable: gitExecutable, RemoteName: profile.Git.Remote, ProfileKey: key,
		}
		discovery, err := inventory.New(reader)
		if err != nil {
			return inventory.DiscoveryResult{Errors: []inventory.DiscoveryError{{
				Code: inventory.ErrorProfileObservationInvalid, Message: "Repository discovery is unavailable.",
			}}}
		}
		return discovery.DiscoverRepository(ctx, profile.Root)
	})
}

func discoverConfiguredRepositories(
	ctx context.Context,
	observedAt time.Time,
	document configuration.Document,
	discover func(context.Context, string, configuration.Repository) inventory.DiscoveryResult,
) repositoryInventory {
	result := repositoryInventory{
		Repositories: []contractv2.Repository{}, Alerts: []contractv2.Alert{},
		Profiles: make(map[string]configuration.Repository), ProfileKeys: make(map[string]string),
		ProfileDigests: make(map[string]string),
		Complete:       true, AttemptedAt: observedAt.UTC(),
	}

	keys := make([]string, 0, len(document.Repositories))
	for key, profile := range document.Repositories {
		if profile.Enabled {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	results := make(chan configuredRepositoryResult, len(keys))
	semaphore := make(chan struct{}, maximumConcurrentRepositoryDiscoveries)
	var workers sync.WaitGroup
	for _, key := range keys {
		profile := document.Repositories[key]
		workers.Add(1)
		go func() {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- configuredRepositoryResult{key: key, profile: profile, discovery: inventory.DiscoveryResult{
					Errors: []inventory.DiscoveryError{{Message: "Repository discovery was canceled."}},
				}}
				return
			}
			results <- configuredRepositoryResult{key: key, profile: profile, discovery: discover(ctx, key, profile)}
		}()
	}
	workers.Wait()
	close(results)

	ordered := make([]configuredRepositoryResult, 0, len(keys))
	for discovered := range results {
		ordered = append(ordered, discovered)
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].key < ordered[right].key })
	for _, discovered := range ordered {
		if discovered.discovery.Repository != nil {
			repository := *discovered.discovery.Repository
			repository.DisplayName = discovered.profile.DisplayName
			runtime := configuredRuntimeCatalog(discovered.profile)
			repository.Runtime = &runtime
			observationTime := observedAt.UTC()
			repository.Observation = &contractv2.RepositoryObservation{
				ObservedAt: &observationTime, LastAttemptAt: observationTime,
			}
			result.Repositories = append(result.Repositories, repository)
			result.Profiles[repository.ID] = discovered.profile
			result.ProfileKeys[repository.ID] = discovered.key
		}
		for _, alert := range discovered.discovery.Alerts {
			result.Alerts = append(result.Alerts, newInventoryAlert(
				observedAt, string(alert.Code), alert.Summary, alert.Severity,
			))
		}
		for _, discoveryError := range discovered.discovery.Errors {
			message := discoveryError.Message
			if message == "" {
				message = "Repository discovery failed."
			}
			result.Alerts = append(result.Alerts, newInventoryAlert(
				observedAt, string(discoveryError.Code), message, "error",
			))
			result.Complete = false
		}
	}
	result.Alerts = deduplicateInventoryAlerts(result.Alerts)
	sort.Slice(result.Alerts, func(left, right int) bool { return result.Alerts[left].ID < result.Alerts[right].ID })
	return result
}

func configuredRuntimeCatalog(profile configuration.Repository) contractv2.RepositoryRuntime {
	runtime := contractv2.RepositoryRuntime{
		DefaultTargetID: profile.DefaultTarget, Targets: []contractv2.RuntimeTarget{}, Services: []contractv2.RuntimeService{},
	}
	targetIDs := make([]string, 0, len(profile.Targets))
	for id := range profile.Targets {
		targetIDs = append(targetIDs, id)
	}
	sort.Strings(targetIDs)
	for _, id := range targetIDs {
		target := profile.Targets[id]
		displayName := target.DisplayName
		if displayName == "" {
			displayName = id
		}
		risk := target.Risk
		if risk == "" {
			risk = "local"
		}
		runtime.Targets = append(runtime.Targets, contractv2.RuntimeTarget{
			ID: id, DisplayName: displayName, Risk: risk, WarnOnStart: target.WarnOnStart,
		})
	}
	serviceIDs := make([]string, 0, len(profile.Services))
	for id := range profile.Services {
		serviceIDs = append(serviceIDs, id)
	}
	sort.Strings(serviceIDs)
	for _, id := range serviceIDs {
		service := profile.Services[id]
		runtime.Services = append(runtime.Services, contractv2.RuntimeService{
			ID: id, DisplayName: service.DisplayName, Kind: service.Kind,
			Available: service.IsAvailable(), UnavailableReason: service.UnavailableReason,
		})
	}
	return runtime
}
