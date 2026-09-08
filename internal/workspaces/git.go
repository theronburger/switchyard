package workspaces

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type GitError struct {
	RepositoryID string
	Reason       string
}

func (err *GitError) Error() string { return "Repository " + err.RepositoryID + ": " + err.Reason }

func ResolvePath(ctx context.Context, path string) (string, error) {
	physical, err := physicalWorkspacePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(physical)
	if errors.Is(err, os.ErrNotExist) {
		return physical, nil
	}
	if err != nil || !info.IsDir() {
		return "", errors.New("Workspace path must identify a directory.")
	}
	root, err := gitOutput(ctx, physical, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("The directory is not inside a Git worktree.")
	}
	return physicalWorkspacePath(strings.TrimSuffix(string(root), "\n"))
}

func Discover(ctx context.Context, repositories []RepositoryConfig) ([]Workspace, error) {
	workspaces := []Workspace{}
	seen := map[string]bool{}
	var failures []error
	for _, repository := range repositories {
		if err := ctx.Err(); err != nil {
			return workspaces, errors.Join(append(failures, err)...)
		}
		found, err := discoverRepository(ctx, repository, "")
		if err != nil {
			failures = append(failures, &GitError{RepositoryID: repository.ID, Reason: err.Error()})
			continue
		}
		for _, workspace := range found {
			if seen[workspace.Path] {
				failures = append(failures, &GitError{RepositoryID: repository.ID, Reason: "This worktree is already configured through another repository."})
				continue
			}
			seen[workspace.Path] = true
			workspaces = append(workspaces, workspace)
		}
	}
	sort.Slice(workspaces, func(i, j int) bool {
		if workspaces[i].RepositoryID != workspaces[j].RepositoryID {
			return workspaces[i].RepositoryID < workspaces[j].RepositoryID
		}
		return workspaces[i].Path < workspaces[j].Path
	})
	return workspaces, errors.Join(failures...)
}

func discoverRepository(ctx context.Context, repository RepositoryConfig, exactPath string) ([]Workspace, error) {
	root, common, _, err := gitRoot(ctx, repository.Path)
	if err != nil {
		return nil, errors.New("Cannot read the configured Git root. Check its directory and Git access.")
	}
	output, err := gitOutput(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, errors.New("Cannot list Git worktrees.")
	}
	workspaces, err := parseGitWorktrees(output, repository.ID)
	if err != nil {
		return nil, err
	}
	if exactPath != "" {
		var selected []Workspace
		for _, workspace := range workspaces {
			physical, err := physicalWorkspacePath(workspace.Path)
			if err == nil && physical == exactPath {
				selected = append(selected, workspace)
			}
		}
		if len(selected) != 1 {
			return nil, errors.New("The exact directory is not registered with this repository.")
		}
		workspaces = selected
	}
	for index := range workspaces {
		workspace := &workspaces[index]
		path, err := physicalWorkspacePath(workspace.Path)
		if err != nil {
			workspace.GitError = "Cannot resolve this worktree directory."
			continue
		}
		workspace.Path = path
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			workspace.Missing = true
		} else if err != nil || !info.IsDir() {
			workspace.GitError = "Cannot read this worktree directory."
			continue
		} else {
			_, workspaceCommon, gitDirectory, err := gitRoot(ctx, path)
			if err != nil || workspaceCommon != common {
				workspace.GitError = "This directory no longer matches its Git worktree registration."
				continue
			}
			workspace.Primary = gitDirectory == common
			status, err := gitOutput(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--ignore-submodules=none")
			if err != nil {
				workspace.GitError = "Cannot check this worktree for local changes."
				continue
			}
			workspace.Dirty = len(status) != 0
		}
		if workspace.Head == "" || strings.Trim(workspace.Head, "0") == "" {
			workspace.GitError = "This worktree has no committed HEAD to check."
			continue
		}
		unpushed, err := gitOutput(ctx, root, "rev-list", "--max-count=1", workspace.Head, "--not", "--remotes", "--")
		if err != nil {
			workspace.GitError = "Cannot check this worktree's commits against remote branches."
			continue
		}
		workspace.Unpushed = len(bytes.TrimSpace(unpushed)) != 0
	}
	return workspaces, nil
}

func parseGitWorktrees(output []byte, repositoryID string) ([]Workspace, error) {
	workspaces := []Workspace{}
	current := Workspace{}
	finish := func() error {
		if current.Path == "" || !absoluteConfigPath(current.Path) {
			return errors.New("Git returned an invalid worktree registration.")
		}
		current.RepositoryID = repositoryID
		current.Choices.Services = []string{}
		workspaces = append(workspaces, current)
		current = Workspace{}
		return nil
	}
	for _, field := range bytes.Split(output, []byte{0}) {
		if len(field) == 0 {
			if current.Path != "" {
				if err := finish(); err != nil {
					return nil, err
				}
			}
			continue
		}
		name, value, _ := strings.Cut(string(field), " ")
		switch name {
		case "worktree":
			if current.Path != "" {
				return nil, errors.New("Git returned an invalid worktree registration.")
			}
			current.Path = value
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "locked":
			current.Locked = true
		case "bare":
			current.Primary = true
			current.GitError = "Bare repositories cannot run services."
		}
	}
	if current.Path != "" {
		if err := finish(); err != nil {
			return nil, err
		}
	}
	if len(workspaces) == 0 {
		return nil, errors.New("Git returned no worktree registrations.")
	}
	// Git guarantees the primary entry first, including when its directory is missing.
	workspaces[0].Primary = true
	return workspaces, nil
}

func gitRoot(ctx context.Context, path string) (root, common, directory string, err error) {
	if !absoluteConfigPath(path) {
		return "", "", "", errors.New("Git directory must be absolute.")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", "", errors.New("Cannot read Git directory.")
	}
	resolved := make([]string, 3)
	for index, option := range []string{"--show-toplevel", "--git-common-dir", "--absolute-git-dir"} {
		output, readErr := gitOutput(ctx, physical, "rev-parse", "--path-format=absolute", option)
		if readErr != nil {
			return "", "", "", readErr
		}
		resolved[index], err = filepath.EvalSymlinks(strings.TrimSuffix(string(output), "\n"))
		if err != nil {
			return "", "", "", errors.New("Cannot resolve Git directory identity.")
		}
	}
	if resolved[0] != physical {
		return "", "", "", errors.New("Configured directory must be an exact Git worktree root.")
	}
	return resolved[0], resolved[1], resolved[2], nil
}

func physicalWorkspacePath(path string) (string, error) {
	if !absoluteConfigPath(path) {
		return "", errors.New("Workspace path must be absolute.")
	}
	parent, suffix := path, ""
	for {
		physical, err := filepath.EvalSymlinks(parent)
		if err == nil {
			return filepath.Join(physical, suffix), nil
		}
		if !errors.Is(err, os.ErrNotExist) || parent == filepath.Dir(parent) {
			return "", errors.New("Cannot resolve workspace path.")
		}
		suffix = filepath.Join(filepath.Base(parent), suffix)
		parent = filepath.Dir(parent)
	}
}

func gitOutput(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(arguments) >= 2 && arguments[0] == "worktree" && (arguments[1] == "remove" || arguments[1] == "add") {
		// Interrupting Git's filesystem mutation can leave a partially removed or created checkout.
		ctx = context.WithoutCancel(ctx)
	} else {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, arguments...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var output cappedGitOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Git command failed.")
	}
	if output.overflow {
		return nil, errors.New("Git output exceeded 2 MB.")
	}
	return output.Bytes(), nil
}

type cappedGitOutput struct {
	bytes.Buffer
	overflow bool
}

func (output *cappedGitOutput) Write(contents []byte) (int, error) {
	const maximum = 2 * 1024 * 1024
	if output.Len()+len(contents) > maximum {
		output.overflow = true
		return len(contents), nil
	}
	_, _ = output.Buffer.Write(contents)
	return len(contents), nil
}

func gitBlockers(workspace Workspace) []string {
	blockers := []string{}
	if workspace.Primary {
		blockers = append(blockers, "The primary worktree cannot be removed.")
	}
	if workspace.Locked {
		blockers = append(blockers, "Git has locked this worktree. Unlock it before removing it.")
	}
	if workspace.Dirty {
		blockers = append(blockers, "This worktree has local changes. Commit or move them before removing it.")
	}
	if workspace.Unpushed {
		blockers = append(blockers, "This worktree has commits not present on a remote branch. Push them before removing it.")
	}
	if workspace.GitError != "" {
		blockers = append(blockers, workspace.GitError)
	}
	return blockers
}

func repositoryWorkspace(ctx context.Context, repository RepositoryConfig, path string) (Workspace, error) {
	physical, err := physicalWorkspacePath(path)
	if err != nil {
		return Workspace{}, err
	}
	workspaces, err := discoverRepository(ctx, repository, physical)
	if err != nil {
		return Workspace{}, err
	}
	return workspaces[0], nil
}
