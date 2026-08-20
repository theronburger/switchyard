package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefinitionValidateEnforcesFiniteVocabulary(t *testing.T) {
	valid := Definition{ID: "tidy", DisplayName: "Tidy", Scope: ScopeWorktree, Risk: RiskLocal, Kind: KindCommand}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Definition){
		"empty id":                 func(d *Definition) { d.ID = "" },
		"shell-looking id":         func(d *Definition) { d.ID = "tidy; rm" },
		"empty display name":       func(d *Definition) { d.DisplayName = "" },
		"unknown scope":            func(d *Definition) { d.Scope = "global" },
		"unknown risk":             func(d *Definition) { d.Risk = "destructive" },
		"unknown kind":             func(d *Definition) { d.Kind = "script" },
		"command with lifecycle":   func(d *Definition) { d.Lifecycle = LifecycleStart },
		"lifecycle without name":   func(d *Definition) { d.Kind = KindLifecycle },
		"lifecycle with bad name":  func(d *Definition) { d.Kind = KindLifecycle; d.Lifecycle = "restart" },
		"lifecycle archive banned": func(d *Definition) { d.Kind = KindLifecycle; d.Lifecycle = "archive" },
	} {
		definition := valid
		mutate(&definition)
		if !errors.Is(definition.Validate(), ErrInvalidDefinition) {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	remoteWrite := Definition{Risk: RiskRemoteWrite}
	remoteRead := Definition{Risk: RiskRemoteRead}
	if !remoteWrite.RequiresConfirmation() || remoteRead.RequiresConfirmation() {
		t.Fatal("only remote-write actions require per-run confirmation")
	}
}

func TestValidateScopeRequiresExactTargets(t *testing.T) {
	cases := []struct {
		scope  string
		target Target
		ok     bool
	}{
		{ScopeMachine, Target{RepositoryID: "r"}, true},
		{ScopeMachine, Target{RepositoryID: "r", WorktreeID: "w"}, false},
		{ScopeRepository, Target{RepositoryID: "r"}, true},
		{ScopeRepository, Target{}, false},
		{ScopeRepository, Target{RepositoryID: "r", EnvironmentID: "e"}, false},
		{ScopeWorktree, Target{RepositoryID: "r", WorktreeID: "w"}, true},
		{ScopeWorktree, Target{RepositoryID: "r"}, false},
		{ScopeWorktree, Target{RepositoryID: "r", WorktreeID: "w", EnvironmentID: "e"}, false},
		{ScopeEnvironment, Target{RepositoryID: "r", EnvironmentID: "e"}, true},
		{ScopeEnvironment, Target{RepositoryID: "r", EnvironmentID: "e", ServiceID: "s"}, false},
		{ScopeEnvironment, Target{RepositoryID: "r", WorktreeID: "w"}, false},
		{ScopeService, Target{RepositoryID: "r", EnvironmentID: "e", ServiceID: "s"}, true},
		{ScopeService, Target{RepositoryID: "r", EnvironmentID: "e"}, false},
		{"global", Target{RepositoryID: "r"}, false},
	}
	for _, c := range cases {
		err := ValidateScope(c.scope, c.target)
		if (err == nil) != c.ok {
			t.Fatalf("scope %s target %+v: err=%v want ok=%v", c.scope, c.target, err, c.ok)
		}
	}
}

func testCommand(t *testing.T, executable string, arguments ...string) ExactCommand {
	t.Helper()
	root := t.TempDir()
	return ExactCommand{
		Executable: executable, Arguments: arguments, Directory: root,
		Environment:  []string{"HOME=" + root, "PATH=/usr/bin:/bin", "TMPDIR=" + root},
		Timeout:      10 * time.Second,
		RunDirectory: filepath.Join(root, "run"),
	}
}

func TestExactRunnerCapturesBoundedOutputAndExitCode(t *testing.T) {
	command := testCommand(t, "/bin/sh", "-c", "head -c 2000000 /dev/zero | tr '\\0' x; echo err >&2; exit 3")
	outcome, err := ExactRunner{}.Run(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ExitCode != 3 || !outcome.StdoutTruncated || outcome.StderrTruncated || outcome.TimedOut {
		t.Fatalf("outcome: %+v", outcome)
	}
	stdout, err := os.Stat(filepath.Join(command.RunDirectory, "stdout.log"))
	if err != nil || stdout.Size() != MaximumOutputBytes || stdout.Mode().Perm() != 0o600 {
		t.Fatalf("stdout log: %v %+v", err, stdout)
	}
	stderr, err := os.ReadFile(filepath.Join(command.RunDirectory, "stderr.log"))
	if err != nil || strings.TrimSpace(string(stderr)) != "err" {
		t.Fatalf("stderr log: %v %q", err, stderr)
	}
}

func TestExactRunnerReportsTimeoutAndStopsProcessGroup(t *testing.T) {
	command := testCommand(t, "/bin/sh", "-c", "sleep 30 & wait")
	command.Timeout = 300 * time.Millisecond
	started := time.Now()
	outcome, err := ExactRunner{}.Run(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.TimedOut || outcome.ExitCode == 0 {
		t.Fatalf("outcome: %+v", outcome)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("runner waited %s for a timed-out process group", elapsed)
	}
}

func TestExactRunnerReportsCancellationAsError(t *testing.T) {
	command := testCommand(t, "/bin/sh", "-c", "sleep 30")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	_, err := ExactRunner{}.Run(ctx, command)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestExactRunnerRejectsUnsafeCommandsWithoutStarting(t *testing.T) {
	root := t.TempDir()
	symlink := filepath.Join(root, "sh")
	if err := os.Symlink("/bin/sh", symlink); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "plain")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*ExactCommand){
		"symlinked executable":      func(c *ExactCommand) { c.Executable = symlink },
		"non-executable file":       func(c *ExactCommand) { c.Executable = script },
		"relative executable":       func(c *ExactCommand) { c.Executable = "sh" },
		"missing directory":         func(c *ExactCommand) { c.Directory = filepath.Join(root, "missing") },
		"zero timeout":              func(c *ExactCommand) { c.Timeout = 0 },
		"excessive timeout":         func(c *ExactCommand) { c.Timeout = MaximumTimeout + time.Second },
		"missing HOME":              func(c *ExactCommand) { c.Environment = []string{"PATH=/bin", "TMPDIR=" + root} },
		"duplicate environment":     func(c *ExactCommand) { c.Environment = append(c.Environment, "HOME=/elsewhere") },
		"nul in argument":           func(c *ExactCommand) { c.Arguments = []string{"a\x00b"} },
		"relative run directory":    func(c *ExactCommand) { c.RunDirectory = "run" },
		"environment without value": func(c *ExactCommand) { c.Environment = append(c.Environment, "BROKEN") },
	}
	for name, mutate := range cases {
		command := testCommand(t, "/bin/sh", "-c", "touch "+filepath.Join(root, "started"))
		mutate(&command)
		_, err := ExactRunner{}.Run(context.Background(), command)
		if !errors.Is(err, ErrInvalidCommand) {
			t.Fatalf("%s: expected ErrInvalidCommand, got %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "started")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an invalid command was started")
	}
}

func TestExactRunnerDoesNotInheritDaemonEnvironment(t *testing.T) {
	t.Setenv("SWITCHYARD_TEST_SECRET", "leak")
	command := testCommand(t, "/bin/sh", "-c", "test -z \"$SWITCHYARD_TEST_SECRET\"")
	outcome, err := ExactRunner{}.Run(context.Background(), command)
	if err != nil || outcome.ExitCode != 0 {
		t.Fatalf("daemon environment leaked: err=%v outcome=%+v", err, outcome)
	}
}
