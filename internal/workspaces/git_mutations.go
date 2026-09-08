package workspaces

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func GitPrunePlan(ctx context.Context, repository RepositoryConfig, path string) (PrunePlan, error) {
	physical, err := physicalWorkspacePath(path)
	if err != nil {
		return PrunePlan{}, err
	}
	plan := PrunePlan{Path: physical, Blockers: []string{}}
	workspace, err := repositoryWorkspace(ctx, repository, physical)
	if err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
		return plan, nil
	}
	plan.Branch = workspace.Branch
	plan.Blockers = gitBlockers(workspace)
	return plan, nil
}

func GitPrune(ctx context.Context, repository RepositoryConfig, path string) error {
	plan, err := GitPrunePlan(ctx, repository, path)
	if err != nil {
		return err
	}
	if len(plan.Blockers) != 0 {
		return errors.New(strings.Join(plan.Blockers, " "))
	}
	if _, err := gitOutput(ctx, repository.Path, "worktree", "remove", "--", plan.Path); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Git refused to remove this worktree. Check for new changes, a lock or submodules, then try again.")
	}
	return nil
}

func GitCreate(ctx context.Context, repository RepositoryConfig, request CreateRequest) (string, error) {
	if request.RepositoryID != repository.ID || request.Branch == "" || len(request.Branch) > 200 ||
		strings.HasPrefix(request.Branch, "-") || strings.ContainsAny(request.Branch, "\r\n\x00") {
		return "", errors.New("Choose a valid branch name and repository.")
	}
	root, _, _, err := gitRoot(ctx, repository.Path)
	if err != nil {
		return "", errors.New("Cannot read the configured Git root.")
	}
	if _, err := gitOutput(ctx, root, "check-ref-format", "refs/heads/"+request.Branch); err != nil {
		return "", errors.New("Git rejected the branch name.")
	}
	base := request.Base
	if base == "" {
		base = "HEAD"
	}
	if strings.HasPrefix(base, "-") || strings.ContainsAny(base, "\r\n\x00") || len(base) > 256 {
		return "", errors.New("Choose a valid starting branch or commit.")
	}
	baseCommit, err := gitOutput(ctx, root, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return "", errors.New("The starting branch or commit does not exist locally.")
	}
	parent := repository.WorktreesPath
	if parent == "" {
		parent = filepath.Join(filepath.Dir(root), filepath.Base(root)+"-worktrees")
	}
	parent, err = physicalWorkspacePath(parent)
	if err != nil {
		return "", err
	}
	if parent == root || strings.HasPrefix(parent, root+string(filepath.Separator)) {
		return "", errors.New("Choose a worktrees directory outside the repository checkout.")
	}
	path := filepath.Join(parent, filepath.FromSlash(request.Branch))
	path, err = physicalWorkspacePath(path)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(path, parent+string(filepath.Separator)) {
		return "", errors.New("The branch directory must remain inside the worktrees directory.")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", errors.New("Cannot create the worktrees parent directory.")
	}
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("The new worktree directory already exists or cannot be read.")
	}
	arguments := []string{"worktree", "add"}
	if _, err := gitOutput(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+request.Branch); err == nil {
		if request.Base != "" {
			return "", errors.New("The branch already exists. Leave the starting point empty to use that branch.")
		}
		arguments = append(arguments, "--", path, request.Branch)
	} else {
		arguments = append(arguments, "-b", request.Branch, "--", path, strings.TrimSpace(string(baseCommit)))
	}
	if _, err := gitOutput(ctx, root, arguments...); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("Git could not create the worktree. The branch may already be checked out or its directory may be registered.")
	}
	created, _, _, err := gitRoot(ctx, path)
	if err != nil {
		return "", errors.New("Git created the worktree but its directory could not be verified.")
	}
	return created, nil
}
