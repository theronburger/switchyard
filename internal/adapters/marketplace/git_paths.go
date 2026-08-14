package marketplace

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
)

type GitRepositoryPaths struct {
	CommonDirectory   string
	SharedExcludePath string
}

func (discovery GitDiscovery) RepositoryPaths(
	ctx context.Context,
	repositoryRoot string,
) (GitRepositoryPaths, error) {
	if discovery.runner == nil {
		return GitRepositoryPaths{}, fmt.Errorf("discover git repository paths: command runner is required")
	}
	if discovery.gitExecutable == "" {
		return GitRepositoryPaths{}, fmt.Errorf("discover git repository paths: git executable is required")
	}
	if repositoryRoot == "" {
		return GitRepositoryPaths{}, fmt.Errorf("discover git repository paths: repository root is required")
	}

	commonDirectory, err := discovery.runAbsoluteGitPath(ctx, repositoryRoot, []string{
		"rev-parse",
		"--path-format=absolute",
		"--git-common-dir",
	})
	if err != nil {
		return GitRepositoryPaths{}, fmt.Errorf("discover git common directory: %w", err)
	}
	sharedExcludePath, err := discovery.runAbsoluteGitPath(ctx, repositoryRoot, []string{
		"rev-parse",
		"--path-format=absolute",
		"--git-path",
		"info/exclude",
	})
	if err != nil {
		return GitRepositoryPaths{}, fmt.Errorf("discover shared git exclude path: %w", err)
	}

	return GitRepositoryPaths{
		CommonDirectory:   commonDirectory,
		SharedExcludePath: sharedExcludePath,
	}, nil
}

func (discovery GitDiscovery) runAbsoluteGitPath(
	ctx context.Context,
	repositoryRoot string,
	arguments []string,
) (string, error) {
	invocationArguments := []string{"-C", repositoryRoot}
	invocationArguments = append(invocationArguments, arguments...)
	output, err := discovery.runner.Run(ctx, Invocation{
		Executable: discovery.gitExecutable,
		Arguments:  invocationArguments,
	})
	if err != nil {
		return "", err
	}
	return parseAbsoluteGitPath(output.Stdout)
}

func parseAbsoluteGitPath(contents []byte) (string, error) {
	if len(contents) == 0 {
		return "", fmt.Errorf("git returned an empty path")
	}
	if bytes.IndexByte(contents, 0) >= 0 {
		return "", fmt.Errorf("git path contains a NUL byte")
	}
	if bytes.Count(contents, []byte{'\n'}) > 1 ||
		(bytes.Count(contents, []byte{'\n'}) == 1 && contents[len(contents)-1] != '\n') {
		return "", fmt.Errorf("git returned more than one path")
	}

	pathBytes := bytes.TrimSuffix(contents, []byte{'\n'})
	pathBytes = bytes.TrimSuffix(pathBytes, []byte{'\r'})
	if len(pathBytes) == 0 {
		return "", fmt.Errorf("git returned an empty path")
	}
	path := string(pathBytes)
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("git returned a non-absolute path")
	}
	return path, nil
}
