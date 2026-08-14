package processhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	helperModeVariable       = "SWITCHYARD_PROCESSHOST_TEST_MODE"
	helperReadyVariable      = "SWITCHYARD_PROCESSHOST_TEST_READY"
	helperChildReadyVariable = "SWITCHYARD_PROCESSHOST_TEST_CHILD_READY"
)

func TestProcessHostHelper(t *testing.T) {
	mode := os.Getenv(helperModeVariable)
	if mode == "" {
		return
	}
	if strings.HasPrefix(mode, "stubborn-") {
		signal.Ignore(syscall.SIGTERM)
	}

	switch mode {
	case "child", "stubborn-child", "foreign":
		fmt.Fprintln(os.Stdout, "helper child stdout")
		fmt.Fprintln(os.Stderr, "helper child stderr")
		if readyPath := os.Getenv(helperChildReadyVariable); readyPath != "" {
			if err := os.WriteFile(readyPath, []byte("ready\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for {
			time.Sleep(time.Hour)
		}
	case "parent", "stubborn-parent":
		childMode := "child"
		if mode == "stubborn-parent" {
			childMode = "stubborn-child"
		}
		readyPath := os.Getenv(helperReadyVariable)
		childReadyPath := readyPath + ".child"
		child := exec.Command(os.Args[0], "-test.run=TestProcessHostHelper")
		child.Env = environmentWith(
			environmentWith(os.Environ(), helperModeVariable, childMode),
			helperChildReadyVariable,
			childReadyPath,
		)
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		waitForHelperFile(t, childReadyPath)
		if err := os.WriteFile(readyPath, []byte(fmt.Sprintf("%d %d\n", os.Getpid(), child.Process.Pid)), 0o600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, "helper parent stdout")
		fmt.Fprintln(os.Stderr, "helper parent stderr")
		if err := child.Wait(); err != nil {
			os.Exit(0)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func waitForHelperFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for child helper readiness")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHostStartsReconcilesObservesAndStopsOwnedDescendants(t *testing.T) {
	runDirectory := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, StdoutLogFileName), []byte("existing stdout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDirectory, StderrLogFileName), []byte("existing stderr\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	host := New(Config{GracePeriod: time.Second, KillWait: time.Second, PollInterval: 10 * time.Millisecond})
	spec := helperLaunchSpec(runDirectory, readyPath, "parent")
	ownership, err := host.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ownershipPath := filepath.Join(runDirectory, OwnershipFileName)
	t.Cleanup(func() { stopOwnedRun(host, ownershipPath) })
	if _, err := os.Lstat(filepath.Join(runDirectory, LaunchIntentFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed launch retained intent: %v", err)
	}

	if ownership.ProcessGroupID != ownership.Leader.PID {
		t.Fatalf("dedicated process group: leader %d, group %d", ownership.Leader.PID, ownership.ProcessGroupID)
	}
	wantLaunchFingerprint := fingerprintCommand(spec.Executable, append([]string{spec.Executable}, spec.Arguments...))
	if ownership.LaunchFingerprint != wantLaunchFingerprint {
		t.Fatal("persisted launch fingerprint does not represent the exact argv")
	}

	waitForFile(t, readyPath)
	observation := waitForMembers(t, host, ownershipPath, 2)
	if observation.MemoryBytes == 0 {
		t.Fatal("group observation did not aggregate resident memory")
	}

	restartedHost := New(Config{GracePeriod: time.Second, KillWait: time.Second, PollInterval: 10 * time.Millisecond})
	reconciled, err := restartedHost.Reconcile(context.Background(), ownershipPath)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.MemberCount < 2 {
		t.Fatalf("reconciled members: got %d, want at least 2", reconciled.MemberCount)
	}

	foreign := startForeignHelper(t)
	stopped, err := host.Stop(context.Background(), ownershipPath)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != "stopped" || stopped.MemberCount != 0 {
		t.Fatalf("stopped observation: %+v", stopped)
	}
	if err := syscall.Kill(foreign.Process.Pid, 0); err != nil {
		t.Fatalf("stopping owned group touched foreign process: %v", err)
	}

	stdout := readEventually(t, ownership.StdoutPath, "helper child stdout")
	stderr := readEventually(t, ownership.StderrPath, "helper child stderr")
	if !strings.HasPrefix(stdout, "existing stdout\n") || !strings.Contains(stdout, "helper parent stdout") {
		t.Fatalf("stdout log was truncated or incomplete: %q", stdout)
	}
	if !strings.HasPrefix(stderr, "existing stderr\n") || !strings.Contains(stderr, "helper parent stderr") {
		t.Fatalf("stderr log was truncated or incomplete: %q", stderr)
	}
	for _, logPath := range []string{ownership.StdoutPath, ownership.StderrPath, ownershipPath} {
		fileInfo, err := os.Stat(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("mode for %s: got %04o, want 0600", logPath, fileInfo.Mode().Perm())
		}
	}
}

func TestStartRefusesAnUnverifiedPriorLaunchIntentWithoutForking(t *testing.T) {
	runDirectory := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	spec := helperLaunchSpec(runDirectory, readyPath, "parent")
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	intent := LaunchIntent{
		SchemaVersion: LaunchIntentSchemaVersion, EnvironmentID: spec.EnvironmentID,
		ServiceID: spec.ServiceID, RunID: spec.RunID, Executable: spec.Executable,
		LaunchFingerprint: fingerprintCommand(spec.Executable, append([]string{spec.Executable}, spec.Arguments...)),
		RunDirectory:      runDirectory, CreatedAt: now, UpdatedAt: now,
	}
	if err := saveLaunchIntent(filepath.Join(runDirectory, LaunchIntentFileName), intent); err != nil {
		t.Fatal(err)
	}

	_, err := New(Config{}).Start(context.Background(), spec)
	if !errors.Is(err, ErrOrphanUnverified) {
		t.Fatalf("start error: got %v, want %v", err, ErrOrphanUnverified)
	}
	if _, err := os.Stat(readyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused relaunch forked helper: %v", err)
	}
}

func TestHostEscalatesVerifiedStubbornGroupToSIGKILL(t *testing.T) {
	runDirectory := filepath.Join(t.TempDir(), "run")
	readyPath := filepath.Join(t.TempDir(), "ready")
	host := New(Config{GracePeriod: 75 * time.Millisecond, KillWait: time.Second, PollInterval: 10 * time.Millisecond})
	_, err := host.Start(context.Background(), helperLaunchSpec(runDirectory, readyPath, "stubborn-parent"))
	if err != nil {
		t.Fatal(err)
	}
	ownershipPath := filepath.Join(runDirectory, OwnershipFileName)
	t.Cleanup(func() { stopOwnedRun(host, ownershipPath) })
	waitForFile(t, readyPath)
	waitForMembers(t, host, ownershipPath, 2)

	if _, err := host.Stop(context.Background(), ownershipPath); err != nil {
		t.Fatal(err)
	}
	exited := waitForOwnershipExit(t, ownershipPath)
	if exited.Exit == nil || exited.Exit.Signal != int(syscall.SIGKILL) {
		t.Fatalf("leader exit: got %+v, want SIGKILL", exited.Exit)
	}
}

func TestStartHonorsCancellationBeforeCreatingRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runDirectory := filepath.Join(t.TempDir(), "cancelled-run")
	_, err := New(Config{}).Start(ctx, helperLaunchSpec(runDirectory, filepath.Join(t.TempDir(), "ready"), "parent"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("start error: got %v, want %v", err, context.Canceled)
	}
	if _, err := os.Stat(runDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled start created a run directory: %v", err)
	}
}

func TestStartRefusesSymlinkLogWithoutLaunching(t *testing.T) {
	runDirectory := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(t.TempDir(), "foreign.log")
	if err := os.WriteFile(targetPath, []byte("foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetPath, filepath.Join(runDirectory, StdoutLogFileName)); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")

	_, err := New(Config{}).Start(context.Background(), helperLaunchSpec(runDirectory, readyPath, "parent"))
	if err == nil {
		t.Fatal("start accepted a symlink stdout log")
	}
	if _, err := os.Stat(readyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused launch started the helper: %v", err)
	}
	contents, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "foreign\n" {
		t.Fatalf("foreign log changed: %q", contents)
	}
}

func helperLaunchSpec(runDirectory, readyPath, mode string) LaunchSpec {
	return LaunchSpec{
		EnvironmentID: "env_test",
		ServiceID:     "service_test",
		RunID:         "run_" + mode,
		Executable:    os.Args[0],
		Arguments:     []string{"-test.run=TestProcessHostHelper"},
		Environment: environmentWith(
			environmentWith(os.Environ(), helperModeVariable, mode),
			helperReadyVariable,
			readyPath,
		),
		Directory:    filepath.Dir(os.Args[0]),
		RunDirectory: runDirectory,
	}
}

func environmentWith(environment []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForMembers(t *testing.T, host *Host, ownershipPath string, minimum int) Observation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		observation, err := host.Observe(context.Background(), ownershipPath)
		if err == nil && observation.MemberCount >= minimum {
			return observation
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d members: observation %+v, error %v", minimum, observation, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readEventually(t *testing.T, path, substring string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		contents, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(contents), substring) {
			return string(contents)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out reading %q from %s: %v", substring, path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForOwnershipExit(t *testing.T, ownershipPath string) Ownership {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ownership, err := LoadOwnership(ownershipPath)
		if err == nil && ownership.Exit != nil {
			return ownership
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for exit record: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startForeignHelper(t *testing.T) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=TestProcessHostHelper")
	command.Env = environmentWith(os.Environ(), helperModeVariable, "foreign")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
	})
	return command
}

func stopOwnedRun(host *Host, ownershipPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = host.Stop(ctx, ownershipPath)
}
