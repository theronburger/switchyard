package statusview

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
)

var (
	ErrInvalidSelector     = errors.New("status selector is invalid")
	ErrWorktreeNotFound    = errors.New("worktree was not found")
	ErrWorktreeAmbiguous   = errors.New("worktree selector is ambiguous")
	ErrEnvironmentNotFound = errors.New("environment was not found")
)

type RepositorySummary struct {
	ID          string                            `json:"id"`
	DisplayName string                            `json:"displayName"`
	RootPath    string                            `json:"rootPath"`
	ProfileKey  string                            `json:"profileKey"`
	Remote      string                            `json:"remote"`
	Runtime     *contractv2.RepositoryRuntime     `json:"runtime,omitempty"`
	Observation *contractv2.RepositoryObservation `json:"observation,omitempty"`
}

type WorktreeContext struct {
	SchemaVersion    int                      `json:"schemaVersion"`
	SnapshotRevision int64                    `json:"snapshotRevision"`
	GeneratedAt      time.Time                `json:"generatedAt"`
	Daemon           contractv2.DaemonStatus  `json:"daemon"`
	Repository       RepositorySummary        `json:"repository"`
	Worktree         contractv2.Worktree      `json:"worktree"`
	Environments     []contractv2.Environment `json:"environments"`
	Operations       []contractv2.Operation   `json:"operations"`
	Alerts           []contractv2.Alert       `json:"alerts"`
}

type EnvironmentStatus struct {
	SchemaVersion    int                     `json:"schemaVersion"`
	SnapshotRevision int64                   `json:"snapshotRevision"`
	GeneratedAt      time.Time               `json:"generatedAt"`
	Daemon           contractv2.DaemonStatus `json:"daemon"`
	Repository       RepositorySummary       `json:"repository"`
	Worktree         contractv2.Worktree     `json:"worktree"`
	Environment      contractv2.Environment  `json:"environment"`
	Operations       []contractv2.Operation  `json:"operations"`
	Alerts           []contractv2.Alert      `json:"alerts"`
}

func WorktreeByPath(snapshot contractv2.StatusSnapshot, path string) (WorktreeContext, error) {
	if path == "" || !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return WorktreeContext{}, ErrInvalidSelector
	}
	wanted := canonicalPath(path)
	type candidate struct {
		repository contractv2.Repository
		worktree   contractv2.Worktree
		length     int
	}
	candidates := make([]candidate, 0, 1)
	longest := -1
	for _, repository := range snapshot.Repositories {
		for _, worktree := range repository.Worktrees {
			root := canonicalPath(worktree.Path)
			if !containsPath(root, wanted) {
				continue
			}
			length := len(root)
			if length > longest {
				candidates = candidates[:0]
				longest = length
			}
			if length == longest {
				candidates = append(candidates, candidate{repository: repository, worktree: worktree, length: length})
			}
		}
	}
	if len(candidates) == 0 {
		return WorktreeContext{}, ErrWorktreeNotFound
	}
	if len(candidates) != 1 {
		return WorktreeContext{}, ErrWorktreeAmbiguous
	}
	return buildWorktreeContext(snapshot, candidates[0].repository, candidates[0].worktree), nil
}

func WorktreeBySelector(snapshot contractv2.StatusSnapshot, selector string) (WorktreeContext, error) {
	if selector == "" || strings.IndexByte(selector, 0) >= 0 {
		return WorktreeContext{}, ErrInvalidSelector
	}
	if filepath.IsAbs(selector) {
		return WorktreeByPath(snapshot, selector)
	}
	type candidate struct {
		repository contractv2.Repository
		worktree   contractv2.Worktree
	}
	candidates := make([]candidate, 0, 1)
	for _, repository := range snapshot.Repositories {
		for _, worktree := range repository.Worktrees {
			if worktree.ID == selector || worktree.Branch == selector {
				candidates = append(candidates, candidate{repository: repository, worktree: worktree})
			}
		}
	}
	if len(candidates) == 0 {
		return WorktreeContext{}, ErrWorktreeNotFound
	}
	if len(candidates) != 1 {
		return WorktreeContext{}, ErrWorktreeAmbiguous
	}
	return buildWorktreeContext(snapshot, candidates[0].repository, candidates[0].worktree), nil
}

func EnvironmentByID(snapshot contractv2.StatusSnapshot, environmentID string) (EnvironmentStatus, error) {
	if environmentID == "" || strings.IndexByte(environmentID, 0) >= 0 {
		return EnvironmentStatus{}, ErrInvalidSelector
	}
	for _, environment := range snapshot.Environments {
		if environment.ID != environmentID {
			continue
		}
		for _, repository := range snapshot.Repositories {
			if repository.ID != environment.RepositoryID {
				continue
			}
			for _, worktree := range repository.Worktrees {
				if worktree.ID != environment.WorktreeID {
					continue
				}
				operations, alerts := relatedState(snapshot, map[string]struct{}{environment.ID: {}})
				return EnvironmentStatus{
					SchemaVersion: snapshot.SchemaVersion, SnapshotRevision: snapshot.SnapshotRevision,
					GeneratedAt: snapshot.GeneratedAt, Daemon: snapshot.Daemon,
					Repository: repositorySummary(repository), Worktree: worktree, Environment: environment,
					Operations: operations, Alerts: alerts,
				}, nil
			}
		}
		return EnvironmentStatus{}, ErrEnvironmentNotFound
	}
	return EnvironmentStatus{}, ErrEnvironmentNotFound
}

func buildWorktreeContext(
	snapshot contractv2.StatusSnapshot,
	repository contractv2.Repository,
	worktree contractv2.Worktree,
) WorktreeContext {
	environments := make([]contractv2.Environment, 0, 1)
	environmentIDs := make(map[string]struct{})
	for _, environment := range snapshot.Environments {
		if environment.RepositoryID == repository.ID && environment.WorktreeID == worktree.ID {
			environments = append(environments, environment)
			environmentIDs[environment.ID] = struct{}{}
		}
	}
	sort.SliceStable(environments, func(left, right int) bool {
		return environments[left].ID < environments[right].ID
	})
	operations, alerts := relatedState(snapshot, environmentIDs)
	return WorktreeContext{
		SchemaVersion: snapshot.SchemaVersion, SnapshotRevision: snapshot.SnapshotRevision,
		GeneratedAt: snapshot.GeneratedAt, Daemon: snapshot.Daemon,
		Repository: repositorySummary(repository), Worktree: worktree, Environments: environments,
		Operations: operations, Alerts: alerts,
	}
}

func relatedState(
	snapshot contractv2.StatusSnapshot,
	environmentIDs map[string]struct{},
) ([]contractv2.Operation, []contractv2.Alert) {
	operations := make([]contractv2.Operation, 0)
	for _, operation := range snapshot.Operations {
		if _, related := environmentIDs[operation.EnvironmentID]; related {
			operations = append(operations, operation)
		}
	}
	sort.SliceStable(operations, func(left, right int) bool {
		if operations[left].UpdatedAt.Equal(operations[right].UpdatedAt) {
			return operations[left].ID > operations[right].ID
		}
		return operations[left].UpdatedAt.After(operations[right].UpdatedAt)
	})
	alerts := make([]contractv2.Alert, 0)
	for _, alert := range snapshot.Alerts {
		if _, related := environmentIDs[alert.EnvironmentID]; related {
			alerts = append(alerts, alert)
		}
	}
	sort.SliceStable(alerts, func(left, right int) bool {
		if alerts[left].LastSeenAt.Equal(alerts[right].LastSeenAt) {
			return alerts[left].ID < alerts[right].ID
		}
		return alerts[left].LastSeenAt.After(alerts[right].LastSeenAt)
	})
	return operations, alerts
}

func repositorySummary(repository contractv2.Repository) RepositorySummary {
	return RepositorySummary{
		ID: repository.ID, DisplayName: repository.DisplayName, RootPath: repository.RootPath,
		ProfileKey: repository.ProfileKey, Remote: repository.Remote, Runtime: repository.Runtime,
		Observation: repository.Observation,
	}
}

func containsPath(root, candidate string) bool {
	if root == "" || !filepath.IsAbs(root) {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func canonicalPath(path string) string {
	cleaned := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err == nil && filepath.IsAbs(resolved) {
		return filepath.Clean(resolved)
	}
	return cleaned
}
