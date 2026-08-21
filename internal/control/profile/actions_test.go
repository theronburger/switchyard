package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/configuration"
	actioncontrol "github.com/theronburger/switchyard/internal/control/action"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
)

func actionRegistration(t *testing.T) Registration {
	t.Helper()
	registration := profileRegistration(t)
	literal := func(value string) configuration.ValueRef { return configuration.ValueRef{Literal: &value} }
	registration.Values = map[string]string{"token-name": "resolved-value"}
	if err := os.MkdirAll(filepath.Join(registration.WorktreeRoot, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	registration.Profile.Actions = map[string]configuration.Action{
		"tidy": {
			DisplayName: "Tidy", Scope: "worktree", Risk: "local",
			Command: &configuration.Command{
				Executable: "/usr/bin/true", WorkingDirectory: "sub", Timeout: "2m",
				Arguments:   []configuration.ValueRef{literal("--fast"), {Value: "token-name"}},
				Environment: map[string]configuration.ValueRef{"MODE": literal("tidy")},
			},
		},
		"ping": {
			DisplayName: "Ping", Scope: "service", Risk: "remote-read",
			Command: &configuration.Command{
				Executable: "/usr/bin/true", WorkingDirectory: ".", Timeout: "30s",
				Arguments: []configuration.ValueRef{{URL: &configuration.URLReference{Purpose: "http", Scheme: "http", Host: "localhost", Path: "/health"}}},
			},
		},
		"audit": {
			DisplayName: "Audit", Scope: "repository", Risk: "remote-write",
			Command: &configuration.Command{
				Executable: "/usr/bin/true", WorkingDirectory: ".", Timeout: "1m",
				Arguments: []configuration.ValueRef{{Target: "missing"}},
			},
		},
		"warm": {DisplayName: "Prepare", Scope: "worktree", Risk: "local", Lifecycle: "prepare"},
	}
	return registration
}

func TestActionDefinitionsProjectWithoutCommands(t *testing.T) {
	registration := actionRegistration(t)
	definitions, err := ActionDefinitions(registration.Profile)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		ids = append(ids, definition.ID)
	}
	if strings.Join(ids, ",") != "audit,ping,tidy,warm" {
		t.Fatalf("definitions: %v", ids)
	}
	if definitions[3].Kind != actioncontrol.KindLifecycle || definitions[3].Lifecycle != "prepare" ||
		definitions[2].Kind != actioncontrol.KindCommand || definitions[2].Lifecycle != "" ||
		!definitions[0].RequiresConfirmation() || definitions[1].RequiresConfirmation() {
		t.Fatalf("definitions: %+v", definitions)
	}
	registration.Profile.Actions["broken"] = configuration.Action{DisplayName: "Broken", Scope: "galaxy", Risk: "local", Lifecycle: "start"}
	if _, err := ActionDefinitions(registration.Profile); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("invalid action accepted: %v", err)
	}
}

func TestCompileActionPinsAcceptedProfileValues(t *testing.T) {
	registration := actionRegistration(t)
	command, err := CompileAction(ActionCompileRequest{Registration: registration, ActionID: "tidy", OperationID: "operation_01"})
	if err != nil {
		t.Fatal(err)
	}
	expectedRun := filepath.Join(registration.RuntimeRoot, "actions", "sample", "operation_01")
	if command.Executable != "/usr/bin/true" || strings.Join(command.Arguments, " ") != "--fast resolved-value" ||
		command.Directory != filepath.Join(registration.WorktreeRoot, "sub") || command.Timeout != 2*time.Minute ||
		command.RunDirectory != expectedRun {
		t.Fatalf("command: %+v", command)
	}
	expectedEnvironment := strings.Join([]string{
		"HOME=" + registration.HomeDirectory, "MODE=tidy", "PATH=/usr/bin:/bin", "TMPDIR=" + registration.TemporaryDirectory,
	}, "\n")
	if strings.Join(command.Environment, "\n") != expectedEnvironment {
		t.Fatalf("environment: %v", command.Environment)
	}
}

func TestCompileActionResolvesServiceLeasesOnlyForServiceScope(t *testing.T) {
	registration := actionRegistration(t)
	lease := portlease.Lease{Key: portlease.Key{EnvironmentID: "environment_01", ServiceID: "web", Purpose: "http"}, Host: "127.0.0.1", Port: 30123}
	command, err := CompileAction(ActionCompileRequest{
		Registration: registration, ActionID: "ping", OperationID: "operation_02", ServiceID: "web", Leases: []portlease.Lease{lease},
	})
	if err != nil || len(command.Arguments) != 1 || command.Arguments[0] != "http://localhost:30123/health" {
		t.Fatalf("service action: err=%v command=%+v", err, command)
	}
	// Without a live lease the URL reference cannot resolve; the action fails closed.
	if _, err := CompileAction(ActionCompileRequest{
		Registration: registration, ActionID: "ping", OperationID: "operation_03", ServiceID: "web",
	}); err == nil {
		t.Fatal("unresolvable lease reference was accepted")
	}
	if _, err := CompileAction(ActionCompileRequest{
		Registration: registration, ActionID: "ping", OperationID: "operation_04", ServiceID: "missing", Leases: []portlease.Lease{lease},
	}); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("unknown service accepted: %v", err)
	}
}

func TestCompileActionFailsClosedForUnresolvableOrNonCommandActions(t *testing.T) {
	registration := actionRegistration(t)
	// Target references need a start target context, which actions never have.
	if _, err := CompileAction(ActionCompileRequest{Registration: registration, ActionID: "audit", OperationID: "operation_05"}); err == nil {
		t.Fatal("target reference resolved without a target")
	}
	for name, request := range map[string]ActionCompileRequest{
		"lifecycle action":   {Registration: registration, ActionID: "warm", OperationID: "operation_06"},
		"unknown action":     {Registration: registration, ActionID: "nope", OperationID: "operation_07"},
		"unsafe operation":   {Registration: registration, ActionID: "tidy", OperationID: "../escape"},
		"empty operation id": {Registration: registration, ActionID: "tidy"},
	} {
		if _, err := CompileAction(request); !errors.Is(err, ErrProfileInvalid) {
			t.Fatalf("%s: expected ErrProfileInvalid, got %v", name, err)
		}
	}
}

func TestCompileActionUsesRepositoryRootForRepositoryScope(t *testing.T) {
	registration := actionRegistration(t)
	registration.RepositoryRoot = filepath.Join(registration.RuntimeRoot, "primary")
	if err := os.MkdirAll(filepath.Join(registration.RepositoryRoot, "tools"), 0o700); err != nil {
		t.Fatal(err)
	}
	literal := "ok"
	registration.Profile.Actions["audit"] = configuration.Action{
		DisplayName: "Audit", Scope: "repository", Risk: "remote-write",
		Command: &configuration.Command{
			Executable: "/usr/bin/true", WorkingDirectory: "tools", Timeout: "1m",
			Arguments: []configuration.ValueRef{{Literal: &literal}},
		},
	}
	command, err := CompileAction(ActionCompileRequest{Registration: registration, ActionID: "audit", OperationID: "operation_08"})
	if err != nil || command.Directory != filepath.Join(registration.RepositoryRoot, "tools") {
		t.Fatalf("repository-scoped command: err=%v command=%+v", err, command)
	}
}

// TestCompileActionRefusesWorkingDirectoryOutsideTheWorktree proves that a
// symlink committed into the checkout cannot redirect an accepted command's
// working directory outside the worktree it was accepted for.
func TestCompileActionRefusesWorkingDirectoryOutsideTheWorktree(t *testing.T) {
	registration := actionRegistration(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(registration.WorktreeRoot, "apps")); err != nil {
		t.Fatal(err)
	}
	literal := "ok"
	registration.Profile.Actions["tidy"] = configuration.Action{
		DisplayName: "Tidy", Scope: "worktree", Risk: "local",
		Command: &configuration.Command{
			Executable: "/usr/bin/true", WorkingDirectory: "apps/web", Timeout: "1m",
			Arguments: []configuration.ValueRef{{Literal: &literal}},
		},
	}
	if err := os.MkdirAll(filepath.Join(outside, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileAction(ActionCompileRequest{Registration: registration, ActionID: "tidy", OperationID: "operation_09"}); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("symlinked working directory was accepted: %v", err)
	}
}
