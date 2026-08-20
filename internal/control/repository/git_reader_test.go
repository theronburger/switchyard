package repository

import (
	"context"
	"reflect"
	"testing"
)

func TestGitReaderProducesGenericInventoryObservation(t *testing.T) {
	outputs := map[string][]byte{
		"rev-parse --path-format=absolute --git-common-dir":        []byte("/tmp/sample/.git\n"),
		"rev-parse --path-format=absolute --git-path info/exclude": []byte("/tmp/sample/.git/info/exclude\n"),
		"remote get-url origin":                                    []byte("git@github.com:example/sample.git\n"),
		"worktree list --porcelain -z":                             []byte("worktree /tmp/sample\x00HEAD 0123456789012345678901234567890123456789\x00branch refs/heads/main\x00\x00"),
		"rev-parse --path-format=absolute --absolute-git-dir":      []byte("/tmp/sample/.git\n"),
	}
	reader := GitReader{GitExecutable: "/usr/bin/git", RemoteName: "origin", ProfileKey: "sample", Run: func(_ context.Context, _ string, argv []string) ([]byte, error) {
		arguments := argv[2:]
		return outputs[join(arguments)], nil
	}}
	observation := reader.ReadRepository(context.Background(), "/tmp/sample")
	if len(observation.Errors) != 0 || observation.ProfileKey != "sample" || observation.Remote != "example/sample" ||
		len(observation.Worktrees) != 1 || !observation.Worktrees[0].IsPrimary {
		t.Fatalf("observation: %+v", observation)
	}
}

func TestParseWorktreesRejectsUnknownAndDuplicateFields(t *testing.T) {
	for _, contents := range [][]byte{
		[]byte("worktree /tmp/sample\x00HEAD 0123456789012345678901234567890123456789\x00branch refs/heads/main\x00mystery\x00\x00"),
		[]byte("worktree /tmp/sample\x00HEAD 0123456789012345678901234567890123456789\x00HEAD 0123456789012345678901234567890123456789\x00branch refs/heads/main\x00\x00"),
	} {
		if _, err := parseWorktrees(contents); err == nil {
			t.Fatal("invalid porcelain was accepted")
		}
	}
}

func TestNormalizeRemote(t *testing.T) {
	tests := map[string]string{
		"git@github.com:Example/Sample.git\n":       "example/sample",
		"https://code.example/Example/Sample.git\n": "code.example/Example/Sample",
	}
	for input, want := range tests {
		got, valid := normalizeRemote([]byte(input))
		if !valid || !reflect.DeepEqual(got, want) {
			t.Fatalf("normalize %q: got %q valid=%t", input, got, valid)
		}
	}
}

func join(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += " "
		}
		result += value
	}
	return result
}
