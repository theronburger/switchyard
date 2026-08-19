package githubstatus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOSRunnerUsesSanitizedNonInteractiveEnvironment(t *testing.T) {
	runner := OSRunner{Environment: []string{
		"HOME=/safe/home",
		"USER=tester",
		"GH_TOKEN=secret-gh-token",
		"GITHUB_TOKEN=secret-github-token",
		"PATH=/hostile/bin",
	}}
	result, err := runner.Run(context.Background(), Invocation{Executable: "/usr/bin/env"})
	if err != nil {
		t.Fatal(err)
	}
	output := string(result.Stdout)
	for _, secret := range []string{"secret-gh-token", "secret-github-token", "GH_TOKEN=", "GITHUB_TOKEN="} {
		if strings.Contains(output, secret) {
			t.Fatalf("sanitized environment leaked %q", secret)
		}
	}
	for _, required := range []string{"HOME=/safe/home", "GH_PROMPT_DISABLED=1", "GH_PAGER=cat"} {
		if !strings.Contains(output, required) {
			t.Fatalf("sanitized environment lacks %q: %s", required, output)
		}
	}
}

func TestOSRunnerBoundsOutput(t *testing.T) {
	runner := OSRunner{MaximumStdoutBytes: 8}
	_, err := runner.Run(context.Background(), Invocation{
		Executable: "/usr/bin/printf",
		Arguments:  []string{"0123456789abcdef"},
	})
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("error: got %v, want %v", err, ErrOutputTooLarge)
	}
}

func TestResolveExecutableAcceptsOnlyConfiguredAbsoluteExecutable(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "gh")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveExecutable(executable)
	if err != nil || resolved != executable {
		t.Fatalf("resolved=%q error=%v", resolved, err)
	}
	if _, err := ResolveExecutable("relative/gh"); !errors.Is(err, ErrExecutableUnavailable) {
		t.Fatalf("relative executable error: got %v", err)
	}
}
