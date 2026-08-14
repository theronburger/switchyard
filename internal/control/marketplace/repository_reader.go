package marketplacecontrol

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	"github.com/theronburger/switchyard/internal/control/inventory"
)

type RepositoryReader struct {
	runner        marketplaceadapter.CommandRunner
	gitExecutable string
	gitDiscovery  marketplaceadapter.GitDiscovery
}

func NewRepositoryReader(
	runner marketplaceadapter.CommandRunner,
	gitExecutable string,
) (RepositoryReader, error) {
	if runner == nil {
		return RepositoryReader{}, fmt.Errorf("Marketplace repository reader requires a command runner")
	}
	if gitExecutable == "" {
		return RepositoryReader{}, fmt.Errorf("Marketplace repository reader requires a Git executable")
	}
	return RepositoryReader{
		runner:        runner,
		gitExecutable: gitExecutable,
		gitDiscovery:  marketplaceadapter.NewGitDiscovery(runner, gitExecutable),
	}, nil
}

func (reader RepositoryReader) ReadRepository(
	ctx context.Context,
	repositoryRoot string,
) inventory.RepositoryObservation {
	observation := inventory.RepositoryObservation{AdapterName: "marketplace"}
	paths, err := reader.gitDiscovery.RepositoryPaths(ctx, repositoryRoot)
	if err != nil {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{
			Code: inventory.ErrorRepositoryGitPathsUnavailable,
		})
		return observation
	}
	observation.CommonDirectory = paths.CommonDirectory
	observation.SharedExcludePath = paths.SharedExcludePath

	remoteOutput, err := reader.runner.Run(ctx, marketplaceadapter.Invocation{
		Executable: reader.gitExecutable,
		Arguments: []string{
			"-C",
			repositoryRoot,
			"remote",
			"get-url",
			"origin",
		},
	})
	if err != nil {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{
			Code: inventory.ErrorRepositoryRemoteUnavailable,
		})
		return observation
	}
	remote, valid := normalizeRemote(remoteOutput.Stdout)
	if !valid {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{
			Code: inventory.ErrorRepositoryRemoteUnavailable,
		})
		return observation
	}
	observation.Remote = remote

	worktrees, err := reader.gitDiscovery.ListWorktrees(ctx, repositoryRoot)
	if err != nil {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{
			Code: inventory.ErrorRepositoryWorktreesUnavailable,
		})
		return observation
	}
	for _, worktree := range worktrees {
		observedWorktree := inventory.WorktreeObservation{
			Path:         worktree.Path,
			Branch:       worktree.Branch,
			HeadRevision: worktree.HeadRevision,
			IsPrimary:    worktree.IsPrimary,
			Detached:     worktree.Detached,
			Bare:         worktree.Bare,
			Locked:       worktree.Locked,
			Prunable:     worktree.Prunable,
		}
		if worktree.Prunable {
			observedWorktree.AdministrativeIdentity = pathFallbackIdentity(worktree.Path)
			observation.Errors = append(observation.Errors, inventory.ErrorObservation{
				Code:         inventory.ErrorWorktreeIdentityUnavailable,
				WorktreePath: worktree.Path,
			})
		} else {
			observedWorktree.AdministrativeIdentity = reader.worktreeAdministrativeIdentity(
				ctx,
				worktree.Path,
			)
			if observedWorktree.AdministrativeIdentity == "" {
				observedWorktree.AdministrativeIdentity = pathFallbackIdentity(worktree.Path)
				observation.Errors = append(observation.Errors, inventory.ErrorObservation{
					Code:         inventory.ErrorWorktreeIdentityUnavailable,
					WorktreePath: worktree.Path,
				})
			}
		}
		if worktree.Prunable {
			observation.Alerts = append(observation.Alerts, inventory.AlertObservation{
				Code:         inventory.AlertWorktreePrunable,
				WorktreePath: worktree.Path,
			})
		}
		if worktree.Bare {
			observation.Alerts = append(observation.Alerts, inventory.AlertObservation{
				Code:         inventory.AlertWorktreeBare,
				WorktreePath: worktree.Path,
			})
		}
		observation.Worktrees = append(observation.Worktrees, observedWorktree)
	}
	return observation
}

func (reader RepositoryReader) worktreeAdministrativeIdentity(
	ctx context.Context,
	worktreePath string,
) string {
	output, err := reader.runner.Run(ctx, marketplaceadapter.Invocation{
		Executable: reader.gitExecutable,
		Arguments: []string{
			"-C",
			worktreePath,
			"rev-parse",
			"--path-format=absolute",
			"--absolute-git-dir",
		},
	})
	if err != nil {
		return ""
	}
	path, valid := parseAbsolutePath(output.Stdout)
	if !valid {
		return ""
	}
	return path
}

func parseAbsolutePath(contents []byte) (string, bool) {
	pathBytes, valid := parseOneLine(contents)
	if !valid {
		return "", false
	}
	path := string(pathBytes)
	if !filepath.IsAbs(path) {
		return "", false
	}
	return filepath.Clean(path), true
}

func parseOneLine(contents []byte) ([]byte, bool) {
	if len(contents) == 0 || bytes.IndexByte(contents, 0) >= 0 ||
		bytes.Count(contents, []byte{'\n'}) > 1 ||
		(bytes.Count(contents, []byte{'\n'}) == 1 && contents[len(contents)-1] != '\n') {
		return nil, false
	}
	line := bytes.TrimSuffix(contents, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 {
		return nil, false
	}
	return line, true
}

func pathFallbackIdentity(worktreePath string) string {
	return "registered-path:" + filepath.Clean(worktreePath)
}
