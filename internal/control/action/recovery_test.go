package action

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

func TestExactRunnerPersistsVerifiedOwnershipForTheRun(t *testing.T) {
	command := testCommand(t, "/bin/sh", "-c", "exit 4")
	outcome, err := testRunner(command).Run(context.Background(), command)
	if err != nil || outcome.ExitCode != 4 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	ownership, err := processhost.LoadOwnership(filepath.Join(command.RunDirectory, processhost.OwnershipFileName))
	if err != nil {
		t.Fatal(err)
	}
	if ownership.EnvironmentID != OwnerScope || ownership.ServiceID != "tidy" || ownership.RunID != "operation_01" ||
		ownership.State != "stopped" || ownership.Exit == nil || ownership.Exit.ExitCode != 4 ||
		ownership.Leader.PID <= 1 || ownership.ProcessGroupID != ownership.Leader.PID {
		t.Fatalf("ownership: %+v", ownership)
	}
	if _, err := os.Lstat(filepath.Join(command.RunDirectory, processhost.LaunchIntentFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("launch intent survived a completed run: %v", err)
	}
}

func TestExactRunnerStopsDescendantsThatOutliveTheLeader(t *testing.T) {
	command := testCommand(t, "/bin/sh", "-c", `sleep 30 & echo $! > "$HOME/child.pid"; exit 0`)
	outcome, err := testRunner(command).Run(context.Background(), command)
	if err != nil || outcome.ExitCode != 0 || outcome.TimedOut {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	pid := readPID(t, filepath.Join(command.Directory, "child.pid"))
	awaitGone(t, pid, "straggling descendant")
}

func TestExactRunnerStopsAReExecingLeaderOnTimeout(t *testing.T) {
	// macOS /bin/sh replaces its own image with bash or zsh after start, so
	// the leader's executable path differs from the one inspected at launch.
	command := testCommand(t, "/bin/sh", "-c", "sleep 30")
	command.Timeout = 300 * time.Millisecond
	outcome, err := testRunner(command).Run(context.Background(), command)
	if err != nil || !outcome.TimedOut {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	ownership, err := processhost.LoadOwnership(filepath.Join(command.RunDirectory, processhost.OwnershipFileName))
	if err != nil || ownership.State != "stopped" {
		t.Fatalf("ownership after timeout: %+v err=%v", ownership, err)
	}
	awaitGone(t, ownership.Leader.PID, "timed-out leader")
}

const (
	crashedDaemonModeVariable   = "SWITCHYARD_TEST_CRASHED_DAEMON_MODE"
	crashedDaemonRunVariable    = "SWITCHYARD_TEST_CRASHED_DAEMON_RUN"
	crashedDaemonScopeVariable  = "SWITCHYARD_TEST_CRASHED_DAEMON_SCOPE"
	crashedDaemonScriptVariable = "SWITCHYARD_TEST_CRASHED_DAEMON_SCRIPT"
)

// TestCrashedDaemonHelper is not a test: re-invoked as a subprocess it plays
// a daemon that started an owned leader and then died without stopping it.
func TestCrashedDaemonHelper(t *testing.T) {
	mode := os.Getenv(crashedDaemonModeVariable)
	if mode == "" {
		t.Skip("helper mode only")
	}
	runDirectory := os.Getenv(crashedDaemonRunVariable)
	host := processhost.New(processhost.Config{LeaderSettleDelay: 50 * time.Millisecond})
	_, err := host.Start(context.Background(), processhost.LaunchSpec{
		EnvironmentID: os.Getenv(crashedDaemonScopeVariable), ServiceID: "tidy", RunID: filepath.Base(runDirectory),
		Executable: "/bin/sh", Arguments: []string{"-c", os.Getenv(crashedDaemonScriptVariable)},
		Directory: runDirectory, RunDirectory: runDirectory, Environment: []string{"PATH=/usr/bin:/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ownershipPath := filepath.Join(runDirectory, processhost.OwnershipFileName)
	if mode == "finished" {
		if _, err := host.WaitExit(context.Background(), ownershipPath); err != nil {
			t.Fatal(err)
		}
	} else {
		// Outlive the settle delay so the re-executed /bin/sh leader's
		// fingerprint is persisted, as any daemon running for more than a
		// second would have.
		time.Sleep(300 * time.Millisecond)
	}
	os.Exit(0)
}

// crashDaemonWith starts a leader from a subprocess daemon that then dies,
// leaving the record behind exactly as a crashed daemon would, and returns
// the persisted ownership.
func crashDaemonWith(t *testing.T, runDirectory, scope, script, mode string) processhost.Ownership {
	t.Helper()
	if err := os.Mkdir(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestCrashedDaemonHelper$")
	helper.Env = append(os.Environ(),
		crashedDaemonModeVariable+"="+mode, crashedDaemonRunVariable+"="+runDirectory,
		crashedDaemonScopeVariable+"="+scope, crashedDaemonScriptVariable+"="+script,
	)
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("crashed daemon helper: %v\n%s", err, output)
	}
	ownership, err := processhost.LoadOwnership(filepath.Join(runDirectory, processhost.OwnershipFileName))
	if err != nil {
		t.Fatal(err)
	}
	return ownership
}

func TestRecoverInterruptedRunsStopsOnlyVerifiedRunningActionGroups(t *testing.T) {
	runtimeRoot := privateTempDir(t)
	actionsRoot := filepath.Join(runtimeRoot, ActionsDirectoryName, "sample")
	if err := os.MkdirAll(actionsRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	// An action the previous daemon left running.
	orphanDirectory := filepath.Join(actionsRoot, "operation_orphan")
	orphan := crashDaemonWith(t, orphanDirectory, OwnerScope, "sleep 30", "running")

	// An action whose leader exited before the crash could record a stop.
	finishedDirectory := filepath.Join(actionsRoot, "operation_finished")
	finished := crashDaemonWith(t, finishedDirectory, OwnerScope, "exit 0", "finished")
	if finished.State != "running" || finished.Exit == nil {
		t.Fatalf("finished record: %+v", finished)
	}

	// A record whose persisted start time no longer matches the live leader:
	// the PID could belong to anyone, so it must be left alone.
	driftedDirectory := filepath.Join(actionsRoot, "operation_drifted")
	drifted := crashDaemonWith(t, driftedDirectory, OwnerScope, "sleep 30", "running")
	rewriteOwnership(t, driftedDirectory, func(ownership *processhost.Ownership) {
		ownership.Leader.StartedAt = ownership.Leader.StartedAt.Add(time.Second)
		ownership.Members[0].StartedAt = ownership.Leader.StartedAt
	})

	// Process evidence that is not a finite action is never this recovery's
	// to act on.
	foreignDirectory := filepath.Join(actionsRoot, "operation_foreign")
	foreign := crashDaemonWith(t, foreignDirectory, "env_live", "sleep 30", "running")

	// A crash between fork and verification leaves only an intent.
	intentDirectory := filepath.Join(actionsRoot, "operation_intent")
	if err := os.Mkdir(intentDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(intentDirectory, processhost.LaunchIntentFileName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A run directory that is not private is not consulted at all.
	exposedDirectory := filepath.Join(actionsRoot, "operation_exposed")
	if err := os.Mkdir(exposedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exposedDirectory, processhost.OwnershipFileName), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		for _, pid := range []int{orphan.Leader.PID, drifted.Leader.PID, foreign.Leader.PID} {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})

	restarted := NewProcessHost()
	report, err := RecoverInterruptedRuns(context.Background(), restarted, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stopped != 2 || report.Unverified != 2 || report.Truncated {
		t.Fatalf("report: %+v", report)
	}
	awaitGone(t, orphan.Leader.PID, "orphaned action leader")
	for name, pid := range map[string]int{"drifted": drifted.Leader.PID, "foreign": foreign.Leader.PID} {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("%s leader was signalled without verified ownership: %v", name, err)
		}
	}
	for _, directory := range []string{orphanDirectory, finishedDirectory} {
		ownership, err := processhost.LoadOwnership(filepath.Join(directory, processhost.OwnershipFileName))
		if err != nil || ownership.State != "stopped" {
			t.Fatalf("%s: ownership=%+v err=%v", directory, ownership, err)
		}
	}
	// Recovery is idempotent: nothing is left to stop.
	report, err = RecoverInterruptedRuns(context.Background(), restarted, runtimeRoot)
	if err != nil || report.Stopped != 0 || report.Unverified != 2 {
		t.Fatalf("second recovery: %+v err=%v", report, err)
	}
}

func TestRecoverInterruptedRunsHandlesMissingAndUnsafeRoots(t *testing.T) {
	runtimeRoot := privateTempDir(t)
	report, err := RecoverInterruptedRuns(context.Background(), NewProcessHost(), runtimeRoot)
	if err != nil || report != (RecoveryReport{}) {
		t.Fatalf("empty runtime: %+v err=%v", report, err)
	}
	if _, err := RecoverInterruptedRuns(context.Background(), nil, runtimeRoot); err == nil {
		t.Fatal("nil host accepted")
	}
	if _, err := RecoverInterruptedRuns(context.Background(), NewProcessHost(), "relative"); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("relative root: %v", err)
	}
	exposed := t.TempDir()
	if err := os.Chmod(exposed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(exposed, ActionsDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverInterruptedRuns(context.Background(), NewProcessHost(), exposed); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("group-readable runtime root: %v", err)
	}
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func rewriteOwnership(t *testing.T, runDirectory string, mutate func(*processhost.Ownership)) {
	t.Helper()
	path := filepath.Join(runDirectory, processhost.OwnershipFileName)
	ownership, err := processhost.LoadOwnership(path)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&ownership)
	payload, err := json.Marshal(ownership)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil || pid <= 1 {
		t.Fatalf("pid file %q: %v", contents, err)
	}
	return pid
}

// awaitGone waits until pid no longer names a process; every leader here is
// a child of the test process and is reaped by the host that started it.
func awaitGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s (pid %d) is still running", what, pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
