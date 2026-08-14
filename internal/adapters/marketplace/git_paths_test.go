package marketplace

import (
	"context"
	"reflect"
	"testing"
)

func TestGitDiscoveryRepositoryPathsUsesExactArguments(t *testing.T) {
	runner := &recordingRunner{outputs: []CommandOutput{
		{Stdout: []byte("/Users/example/Marketplace Repo/.git\n")},
		{Stdout: []byte("/Users/example/Marketplace Repo/.git/info/exclude\n")},
	}}
	discovery := NewGitDiscovery(runner, "/usr/bin/git")

	paths, err := discovery.RepositoryPaths(context.Background(), "/Users/example/Marketplace Repo")
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := GitRepositoryPaths{
		CommonDirectory:   "/Users/example/Marketplace Repo/.git",
		SharedExcludePath: "/Users/example/Marketplace Repo/.git/info/exclude",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths: got %#v, want %#v", paths, wantPaths)
	}
	wantInvocations := []Invocation{
		{
			Executable: "/usr/bin/git",
			Arguments: []string{
				"-C",
				"/Users/example/Marketplace Repo",
				"rev-parse",
				"--path-format=absolute",
				"--git-common-dir",
			},
		},
		{
			Executable: "/usr/bin/git",
			Arguments: []string{
				"-C",
				"/Users/example/Marketplace Repo",
				"rev-parse",
				"--path-format=absolute",
				"--git-path",
				"info/exclude",
			},
		},
	}
	if !reflect.DeepEqual(runner.invocations, wantInvocations) {
		t.Fatalf("invocations: got %#v, want %#v", runner.invocations, wantInvocations)
	}
}

func TestParseAbsoluteGitPathRejectsMalformedOutput(t *testing.T) {
	tests := map[string][]byte{
		"empty":         {},
		"blank":         {'\n'},
		"relative":      []byte(".git\n"),
		"multiple":      []byte("/first\n/second\n"),
		"embedded line": []byte("/first\n/second"),
		"nul":           []byte("/first\x00second\n"),
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseAbsoluteGitPath(contents); err == nil {
				t.Fatal("expected malformed path output to fail")
			}
		})
	}
}

func TestGitDiscoveryRepositoryPathsRejectsMissingDependencies(t *testing.T) {
	tests := map[string]GitDiscovery{
		"runner":     NewGitDiscovery(nil, "/usr/bin/git"),
		"executable": NewGitDiscovery(&recordingRunner{}, ""),
	}
	for name, discovery := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := discovery.RepositoryPaths(context.Background(), "/repo"); err == nil {
				t.Fatal("expected missing dependency to fail")
			}
		})
	}
}
