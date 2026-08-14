package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
	controlconfig "github.com/theronburger/switchyard/internal/control/config"
	"github.com/theronburger/switchyard/internal/control/inventory"
	marketplacecontrol "github.com/theronburger/switchyard/internal/control/marketplace"
)

const repositoryRootOverride = "SWITCHYARD_REPOSITORY_ROOT"
const gitExecutableOverride = "SWITCHYARD_GIT_EXECUTABLE"

type repositoryInventory struct {
	Repositories []contractv1.Repository
	Alerts       []contractv1.Alert
	Complete     bool
}

func discoverRepositoryInventory(ctx context.Context, observedAt time.Time) repositoryInventory {
	rootPath := os.Getenv(repositoryRootOverride)
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
	result := repositoryInventory{Repositories: []contractv1.Repository{}, Alerts: []contractv1.Alert{}}
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
		repository.DisplayName = resolution.Repositories[0].DisplayName
		result.Repositories = append(result.Repositories, repository)
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
		repositories := append([]contractv1.Repository(nil), discovered.Repositories...)
		knownRepositories := make(map[string]struct{}, len(repositories))
		for _, repository := range repositories {
			knownRepositories[repository.ID] = struct{}{}
		}
		for _, environment := range snapshot.Environments {
			if _, found := knownRepositories[environment.RepositoryID]; found {
				continue
			}
			for _, previous := range snapshot.Repositories {
				if previous.ID == environment.RepositoryID {
					repositories = append(repositories, previous)
					knownRepositories[previous.ID] = struct{}{}
					break
				}
			}
		}
		sort.Slice(repositories, func(left, right int) bool {
			return repositories[left].ID < repositories[right].ID
		})
		snapshot.Repositories = repositories
	} else if snapshot.Repositories == nil {
		snapshot.Repositories = []contractv1.Repository{}
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
