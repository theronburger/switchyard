package marketplace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseWorktreePorcelain(t *testing.T) {
	contents := readEscapedNULFixture(t, "git", "worktrees.porcelain.txt")

	worktrees, err := ParseWorktreePorcelain(contents)
	if err != nil {
		t.Fatal(err)
	}

	want := []Worktree{
		{
			Path:         "/Users/example/Developer/marketplace",
			HeadRevision: "1111111111111111111111111111111111111111",
			Branch:       "main",
			IsPrimary:    true,
		},
		{
			Path:         "/Users/example/Developer/marketplace-worktrees/DEMO 42 chapter import",
			HeadRevision: "2222222222222222222222222222222222222222",
			Branch:       "feature/DEMO-42/chapter-import",
			Locked:       true,
			LockReason:   "agent owns this worktree",
		},
		{
			Path:           "/Users/example/Developer/marketplace-worktrees/detached review",
			HeadRevision:   "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
			Detached:       true,
			Prunable:       true,
			PrunableReason: "gitdir file points to non-existent location",
		},
	}
	if !reflect.DeepEqual(worktrees, want) {
		t.Fatalf("worktrees:\n got: %#v\nwant: %#v", worktrees, want)
	}
}

func TestParseWorktreePorcelainRejectsMalformedInput(t *testing.T) {
	tests := map[string]string{
		"not NUL terminated":          "worktree /repo\x00HEAD 1111111111111111111111111111111111111111",
		"field before worktree":       "HEAD 1111111111111111111111111111111111111111\x00\x00",
		"missing HEAD":                "worktree /repo\x00branch refs/heads/main\x00\x00",
		"bad HEAD":                    "worktree /repo\x00HEAD not-a-revision\x00branch refs/heads/main\x00\x00",
		"conflicting checkout states": "worktree /repo\x00HEAD 1111111111111111111111111111111111111111\x00branch refs/heads/main\x00detached\x00\x00",
		"unknown field":               "worktree /repo\x00HEAD 1111111111111111111111111111111111111111\x00branch refs/heads/main\x00future value\x00\x00",
	}

	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseWorktreePorcelain([]byte(contents)); err == nil {
				t.Fatal("expected malformed input to fail")
			}
		})
	}
}

func TestGitDiscoveryUsesPorcelainZInvocation(t *testing.T) {
	runner := &recordingRunner{outputs: []CommandOutput{{
		Stdout: readEscapedNULFixture(t, "git", "worktrees.porcelain.txt"),
	}}}
	discovery := NewGitDiscovery(runner, "/usr/bin/git")

	worktrees, err := discovery.ListWorktrees(context.Background(), "/repo with spaces")
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 3 {
		t.Fatalf("worktree count: got %d, want 3", len(worktrees))
	}

	want := Invocation{
		Executable: "/usr/bin/git",
		Arguments: []string{
			"-C",
			"/repo with spaces",
			"worktree",
			"list",
			"--porcelain",
			"-z",
		},
	}
	if !reflect.DeepEqual(runner.invocations, []Invocation{want}) {
		t.Fatalf("invocations:\n got: %#v\nwant: %#v", runner.invocations, []Invocation{want})
	}
}

func readEscapedNULFixture(t *testing.T, parts ...string) []byte {
	t.Helper()
	pathParts := append([]string{"testdata"}, parts...)
	contents, err := os.ReadFile(filepath.Join(pathParts...))
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	return []byte(strings.ReplaceAll(strings.Join(lines, ""), `\0`, "\x00"))
}
