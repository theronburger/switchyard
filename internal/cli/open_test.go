package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestApplicationOpenResolvesStatusSelectorsAndLaunchesOpaqueWorktreeURL(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		getwd    func() (string, error)
		wantURL  string
	}{
		{name: "opaque ID", selector: "worktree_feature", wantURL: "switchyard://worktrees/worktree_feature"},
		{name: "branch", selector: "feature/a", wantURL: "switchyard://worktrees/worktree_feature"},
		{name: "absolute child path", selector: "/Developer/worktrees/feature-a/services/api", wantURL: "switchyard://worktrees/worktree_feature"},
		{name: "current directory", selector: ".", getwd: func() (string, error) {
			return "/Developer/worktrees/feature-a/services/api", nil
		}, wantURL: "switchyard://worktrees/worktree_feature"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var launchedURL string
			var stdout bytes.Buffer
			application := Application{
				Backend: stubBackend{snapshot: cliStatusSnapshot()},
				Stdout:  &stdout,
				Getwd:   test.getwd,
				LaunchURL: func(_ context.Context, candidate string) error {
					launchedURL = candidate
					return nil
				},
			}
			if code := application.Run(context.Background(), []string{"open", test.selector}); code != ExitSuccess {
				t.Fatalf("exit code: got %d", code)
			}
			if launchedURL != test.wantURL {
				t.Fatalf("launched URL: got %q, want %q", launchedURL, test.wantURL)
			}
			if stdout.String() != "Opened Switchyard worktree.\n" {
				t.Fatalf("stdout: %q", stdout.String())
			}
		})
	}
}

func TestApplicationOpenRefusesUnknownOrAmbiguousSelectorsWithoutLaunching(t *testing.T) {
	snapshot := cliStatusSnapshot()
	snapshot.Repositories[0].Worktrees = append(snapshot.Repositories[0].Worktrees,
		snapshot.Repositories[0].Worktrees[1])
	snapshot.Repositories[0].Worktrees[2].ID = "worktree_duplicate"

	for _, selector := range []string{"missing", "feature/a"} {
		t.Run(selector, func(t *testing.T) {
			launches := 0
			var stderr bytes.Buffer
			application := Application{
				Backend: stubBackend{snapshot: snapshot},
				Stderr:  &stderr,
				LaunchURL: func(context.Context, string) error {
					launches++
					return nil
				},
			}
			if code := application.Run(context.Background(), []string{"open", selector}); code != ExitFailure {
				t.Fatalf("exit code: got %d", code)
			}
			if launches != 0 {
				t.Fatalf("launcher called %d times", launches)
			}
			if !strings.Contains(stderr.String(), "worktree") {
				t.Fatalf("stderr: %q", stderr.String())
			}
		})
	}
}

func TestApplicationOpenReportsLaunchFailureWithoutLeakingDetails(t *testing.T) {
	secret := "launch-detail-that-must-not-leak"
	var stderr bytes.Buffer
	application := Application{
		Backend: stubBackend{snapshot: cliStatusSnapshot()},
		Stderr:  &stderr,
		LaunchURL: func(context.Context, string) error {
			return errors.New(secret)
		},
	}
	if code := application.Run(context.Background(), []string{"open", "worktree_feature"}); code != ExitFailure {
		t.Fatalf("exit code: got %d", code)
	}
	if strings.Contains(stderr.String(), secret) {
		t.Fatalf("stderr leaked launch details: %q", stderr.String())
	}
}

func TestSwitchyardWorktreeURLPercentEncodesOneOpaquePathSegment(t *testing.T) {
	worktreeURL, err := switchyardWorktreeURL("worktree/a?b#c")
	if err != nil {
		t.Fatal(err)
	}
	if worktreeURL != "switchyard://worktrees/worktree%2Fa%3Fb%23c" {
		t.Fatalf("URL: got %q", worktreeURL)
	}
	for _, invalidID := range []string{"", " worktree", "worktree\x00value", strings.Repeat("a", 257)} {
		if _, err := switchyardWorktreeURL(invalidID); err == nil {
			t.Fatalf("accepted invalid ID %q", invalidID)
		}
	}
}

func TestSystemOpenCommandUsesExactExecutableAndSingleURLArgument(t *testing.T) {
	command := systemOpenCommand(context.Background(), "switchyard://worktrees/worktree_01")
	if command.Path != systemOpenExecutable {
		t.Fatalf("executable: got %q, want %q", command.Path, systemOpenExecutable)
	}
	if len(command.Args) != 2 || command.Args[0] != systemOpenExecutable ||
		command.Args[1] != "switchyard://worktrees/worktree_01" {
		t.Fatalf("arguments: %#v", command.Args)
	}
}

func TestApplicationRejectsMalformedOpenArguments(t *testing.T) {
	for _, arguments := range [][]string{
		{"open"},
		{"open", ".", "extra"},
		{"open", ".", "--json"},
		{"open", ".", "--wait"},
	} {
		launches := 0
		application := Application{
			Backend: stubBackend{},
			LaunchURL: func(context.Context, string) error {
				launches++
				return nil
			},
		}
		if code := application.Run(context.Background(), arguments); code != ExitUsage {
			t.Fatalf("arguments=%v exit=%d", arguments, code)
		}
		if launches != 0 {
			t.Fatalf("arguments=%v launched %d times", arguments, launches)
		}
	}
}
