package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	workspacecontrol "github.com/theronburger/switchyard/internal/control/workspace"
	"github.com/theronburger/switchyard/internal/state"
)

const gitExecutableOverride = "SWITCHYARD_GIT_EXECUTABLE"

type repositoryInventory struct {
	Repositories                []contractv2.Repository
	Alerts                      []contractv2.Alert
	Profiles                    map[string]configuration.Repository
	ProfileKeys                 map[string]string
	ProfileDigests              map[string]string
	AcceptedConfigurationDigest string
	FirstPort                   int
	LastPort                    int
	InheritedEnvironment        []string
	Complete                    bool
	AttemptedAt                 time.Time
}

func restoreWorkspaceInventory(ctx context.Context, store *state.Store, inventory *repositoryInventory) error {
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
				if worktree.ID == result.WorktreeID {
					worktree.Workspace = contractWorkspaceStatus(result)
				}
			}
		}
	}
	return nil
}

// restoreOccupancyInventory re-attaches durable held handoff leases to freshly
// discovered worktrees so an inventory rebuild never drops occupancy.
func restoreOccupancyInventory(ctx context.Context, store *state.Store, inventory *repositoryInventory) error {
	held, err := store.ListHeldOccupancy(ctx)
	if err != nil {
		return err
	}
	projection := contractv2.StatusSnapshot{Repositories: inventory.Repositories}
	state.ProjectOccupancy(&projection, held)
	inventory.Repositories = projection.Repositories
	return nil
}

func contractWorkspaceStatus(result workspacecontrol.Result) *contractv2.WorkspaceStatus {
	toolchains := make([]contractv2.WorkspaceToolchain, len(result.Toolchains))
	for index, toolchain := range result.Toolchains {
		toolchains[index] = contractv2.WorkspaceToolchain{
			ID: toolchain.ID, RequestedVersion: toolchain.RequestedVersion, ResolvedVersion: toolchain.ResolvedVersion,
		}
	}
	sort.Slice(toolchains, func(left, right int) bool { return toolchains[left].ID < toolchains[right].ID })
	return &contractv2.WorkspaceStatus{
		Ownership: string(result.Ownership), State: string(result.State), Fingerprint: result.Fingerprint,
		PreparedAt: result.PreparedAt, Toolchains: toolchains,
	}
}

func deduplicateInventoryAlerts(alerts []contractv2.Alert) []contractv2.Alert {
	byID := make(map[string]contractv2.Alert, len(alerts))
	for _, alert := range alerts {
		previous, exists := byID[alert.ID]
		if !exists || (previous.Severity != "error" && alert.Severity == "error") {
			byID[alert.ID] = alert
		}
	}
	unique := make([]contractv2.Alert, 0, len(byID))
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
		Repositories: []contractv2.Repository{}, Alerts: []contractv2.Alert{newInventoryAlert(observedAt, code, summary, "error")},
		AttemptedAt: observedAt.UTC(),
	}
}

func newInventoryAlert(observedAt time.Time, code, summary, severity string) contractv2.Alert {
	digest := sha256.Sum256([]byte(code))
	return contractv2.Alert{
		ID: "alert_inventory_" + base64.RawURLEncoding.EncodeToString(digest[:12]), Severity: severity, Code: code,
		Summary: summary, Status: "active", FirstSeenAt: observedAt.UTC(), LastSeenAt: observedAt.UTC(), Occurrences: 1,
	}
}

func mergeRepositoryInventory(snapshot contractv2.StatusSnapshot, discovered repositoryInventory) contractv2.StatusSnapshot {
	if discovered.Complete {
		repositories := append([]contractv2.Repository{}, discovered.Repositories...)
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
			if previous, found := findPreviousWorktree(snapshot.Repositories, environment.RepositoryID, environment.WorktreeID); found {
				repositories[repositoryIndex].Worktrees = append(repositories[repositoryIndex].Worktrees, previous)
			}
		}
		for repositoryIndex := range repositories {
			sort.Slice(repositories[repositoryIndex].Worktrees, func(left, right int) bool {
				return repositories[repositoryIndex].Worktrees[left].ID < repositories[repositoryIndex].Worktrees[right].ID
			})
		}
		sort.Slice(repositories, func(left, right int) bool { return repositories[left].ID < repositories[right].ID })
		snapshot.Repositories = repositories
	} else {
		markRepositoryObservationsStale(snapshot.Repositories, discovered)
		if snapshot.Repositories == nil {
			snapshot.Repositories = []contractv2.Repository{}
		}
	}

	alerts := make([]contractv2.Alert, 0, len(snapshot.Alerts)+len(discovered.Alerts))
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

func repositoryContainsWorktree(repository contractv2.Repository, worktreeID string) bool {
	for _, worktree := range repository.Worktrees {
		if worktree.ID == worktreeID {
			return true
		}
	}
	return false
}

func findPreviousWorktree(repositories []contractv2.Repository, repositoryID, worktreeID string) (contractv2.Worktree, bool) {
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
	return contractv2.Worktree{}, false
}

func markRepositoryObservationsStale(repositories []contractv2.Repository, discovered repositoryInventory) {
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
		var previousObservedAt *time.Time
		if repositories[repositoryIndex].Observation != nil {
			previousObservedAt = repositories[repositoryIndex].Observation.ObservedAt
		}
		repositories[repositoryIndex].Observation = &contractv2.RepositoryObservation{
			ObservedAt: previousObservedAt, LastAttemptAt: attemptedAt, Stale: true, ErrorCode: errorCode,
		}
	}
}

func preservePullRequestObservations(discovered, previous []contractv2.Repository) {
	type worktreeIdentity struct {
		repositoryID string
		worktreeID   string
		branch       string
		headRevision string
	}
	observations := make(map[worktreeIdentity]*contractv2.PullRequestObservation)
	for _, repository := range previous {
		for _, worktree := range repository.Worktrees {
			if worktree.PullRequest != nil {
				observations[worktreeIdentity{repository.ID, worktree.ID, worktree.Branch, worktree.HeadRevision}] = worktree.PullRequest
			}
		}
	}
	for repositoryIndex := range discovered {
		for worktreeIndex := range discovered[repositoryIndex].Worktrees {
			worktree := &discovered[repositoryIndex].Worktrees[worktreeIndex]
			worktree.PullRequest = observations[worktreeIdentity{discovered[repositoryIndex].ID, worktree.ID, worktree.Branch, worktree.HeadRevision}]
		}
	}
}
