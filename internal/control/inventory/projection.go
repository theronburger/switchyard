package inventory

import (
	"path/filepath"
	"sort"
	"strings"

	contractv1 "github.com/theronburger/switchyard/internal/contract/v1"
)

func projectRepository(
	observation RepositoryObservation,
) (contractv1.Repository, map[string]string, bool) {
	if !validAdapterName(observation.AdapterName) || !validRemoteIdentity(observation.Remote) ||
		!filepath.IsAbs(observation.CommonDirectory) ||
		!filepath.IsAbs(observation.SharedExcludePath) ||
		filepath.Clean(observation.SharedExcludePath) != filepath.Join(
			filepath.Clean(observation.CommonDirectory),
			"info",
			"exclude",
		) || len(observation.Worktrees) == 0 {
		return contractv1.Repository{}, nil, false
	}

	repositoryIdentity := repositoryID(filepath.Clean(observation.CommonDirectory), observation.Remote)
	worktrees := make([]contractv1.Worktree, 0, len(observation.Worktrees))
	worktreeIDs := make(map[string]string, len(observation.Worktrees))
	administrativeIdentities := make(map[string]struct{}, len(observation.Worktrees))
	primaryRoot := ""
	primaryCount := 0
	for _, observedWorktree := range observation.Worktrees {
		if !validWorktreeObservation(observedWorktree) {
			return contractv1.Repository{}, nil, false
		}
		if _, duplicate := worktreeIDs[observedWorktree.Path]; duplicate {
			return contractv1.Repository{}, nil, false
		}
		if _, duplicate := administrativeIdentities[observedWorktree.AdministrativeIdentity]; duplicate {
			return contractv1.Repository{}, nil, false
		}
		administrativeIdentities[observedWorktree.AdministrativeIdentity] = struct{}{}

		identity := worktreeID(repositoryIdentity, observedWorktree.AdministrativeIdentity)
		worktreeIDs[observedWorktree.Path] = identity
		if observedWorktree.IsPrimary {
			primaryCount++
			primaryRoot = observedWorktree.Path
		}
		worktrees = append(worktrees, contractv1.Worktree{
			ID:           identity,
			Path:         observedWorktree.Path,
			Branch:       observedWorktree.Branch,
			HeadRevision: observedWorktree.HeadRevision,
			IsPrimary:    observedWorktree.IsPrimary,
			Git: contractv1.WorktreeState{
				Locked:   observedWorktree.Locked,
				Prunable: observedWorktree.Prunable,
			},
		})
	}
	if primaryCount != 1 {
		return contractv1.Repository{}, nil, false
	}

	sort.Slice(worktrees, func(left, right int) bool {
		if worktrees[left].IsPrimary != worktrees[right].IsPrimary {
			return worktrees[left].IsPrimary
		}
		return worktrees[left].Path < worktrees[right].Path
	})
	return contractv1.Repository{
		ID:          repositoryIdentity,
		DisplayName: filepath.Base(primaryRoot),
		RootPath:    primaryRoot,
		Adapter:     observation.AdapterName,
		Remote:      observation.Remote,
		Worktrees:   worktrees,
	}, worktreeIDs, true
}

func validWorktreeObservation(worktree WorktreeObservation) bool {
	if !filepath.IsAbs(worktree.Path) || worktree.AdministrativeIdentity == "" ||
		!isGitObjectID(worktree.HeadRevision) {
		return false
	}
	checkoutStates := 0
	if worktree.Branch != "" {
		checkoutStates++
	}
	if worktree.Detached {
		checkoutStates++
	}
	if worktree.Bare {
		checkoutStates++
	}
	return checkoutStates == 1
}

func isGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}

func validAdapterName(adapterName string) bool {
	if adapterName == "" || adapterName[0] < 'a' || adapterName[0] > 'z' {
		return false
	}
	for _, character := range adapterName[1:] {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func validRemoteIdentity(remote string) bool {
	if remote == "" || !strings.ContainsRune(remote, '/') ||
		strings.Contains(remote, "://") || strings.ContainsAny(remote, "@:?#[\\] \t\r\n") {
		return false
	}
	for _, segment := range strings.Split(remote, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
