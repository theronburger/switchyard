package finiterun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestRunnerReportsExitAndRemovesVerifiedEvidence(t *testing.T) {
	runtimeRoot := privateTempDir(t)
	var stdout, stderr bytes.Buffer
	outcome, err := Runner{RuntimeRoot: runtimeRoot}.Run(context.Background(), testSpec(t, &stdout, &stderr, "echo out; echo err >&2; exit 4"))
	if err != nil || outcome.ExitCode != 4 || outcome.TimedOut || outcome.Signal != 0 {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	assertNoLaunches(t, runtimeRoot)
}

func TestRunnerStopsDescendantsThatOutliveTheLeader(t *testing.T) {
	runtimeRoot := privateTempDir(t)
	var stdout, stderr bytes.Buffer
	spec := testSpec(t, &stdout, &stderr, `sleep 30 & echo $! > "$HOME/child.pid"; exit 0`)
	outcome, err := Runner{RuntimeRoot: runtimeRoot}.Run(context.Background(), spec)
	if err != nil || outcome.ExitCode != 0 || outcome.TimedOut {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	awaitGone(t, readPID(t, filepath.Join(spec.Directory, "child.pid")), "straggling descendant")
	assertNoLaunches(t, runtimeRoot)
}

func TestRunnerStopsAReExecingLeaderOnTimeout(t *testing.T) {
	runtimeRoot := privateTempDir(t)
	var stdout, stderr bytes.Buffer
	spec := testSpec(t, &stdout, &stderr, `echo $$ > "$HOME/leader.pid"; sleep 30`)
	spec.Timeout = 300 * time.Millisecond
	outcome, err := Runner{RuntimeRoot: runtimeRoot}.Run(context.Background(), spec)
	if err != nil || !outcome.TimedOut {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	awaitGone(t, readPID(t, filepath.Join(spec.Directory, "leader.pid")), "timed-out leader")
	assertNoLaunches(t, runtimeRoot)
}

func TestRunnerReportsCancellationAndStopsTheGroup(t *testing.T) {
	runtimeRoot := privateTempDir(t)
	var stdout, stderr bytes.Buffer
	spec := testSpec(t, &stdout, &stderr, `echo $$ > "$HOME/leader.pid"; sleep 30`)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(spec.Directory, "leader.pid")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	_, err := Runner{RuntimeRoot: runtimeRoot}.Run(ctx, spec)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run: %v", err)
	}
	awaitGone(t, readPID(t, filepath.Join(spec.Directory, "leader.pid")), "cancelled leader")
	assertNoLaunches(t, runtimeRoot)
}

func TestRunnerRefusesUnsafeSpecsAndRootsWithoutStarting(t *testing.T) {
	runtimeRoot := privateTempDir(t)
	var stdout, stderr bytes.Buffer
	valid := testSpec(t, &stdout, &stderr, "exit 0")
	cases := map[string]func(*Spec){
		"empty id":          func(spec *Spec) { spec.ID = "" },
		"slash in id":       func(spec *Spec) { spec.ID = "../escape" },
		"relative exe":      func(spec *Spec) { spec.Executable = "sh" },
		"missing exe":       func(spec *Spec) { spec.Executable = filepath.Join(runtimeRoot, "missing") },
		"missing directory": func(spec *Spec) { spec.Directory = filepath.Join(runtimeRoot, "missing") },
		"no timeout":        func(spec *Spec) { spec.Timeout = 0 },
		"huge timeout":      func(spec *Spec) { spec.Timeout = MaximumTimeout + time.Second },
		"nul argument":      func(spec *Spec) { spec.Arguments = []string{"a\x00b"} },
		"bad environment":   func(spec *Spec) { spec.Environment = []string{"NOEQUALS"} },
		"no stdout":         func(spec *Spec) { spec.Stdout = nil },
	}
	for name, mutate := range cases {
		spec := valid
		mutate(&spec)
		if _, err := (Runner{RuntimeRoot: runtimeRoot}).Run(context.Background(), spec); !errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	assertNoLaunches(t, runtimeRoot)

	for name, root := range map[string]string{"relative": "relative", "missing": filepath.Join(runtimeRoot, "missing")} {
		if _, err := (Runner{RuntimeRoot: root}).Run(context.Background(), valid); !errors.Is(err, ErrInvalidRoot) {
			t.Fatalf("%s root: %v", name, err)
		}
	}
	exposed := t.TempDir()
	if err := os.Chmod(exposed, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := (Runner{RuntimeRoot: exposed}).Run(context.Background(), valid); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("group-readable root: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(exposed, DirectoryName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("evidence tree created under an exposed root: %v", err)
	}
	symlinked := privateTempDir(t)
	if err := os.Symlink(runtimeRoot, filepath.Join(symlinked, DirectoryName)); err != nil {
		t.Fatal(err)
	}
	if _, err := (Runner{RuntimeRoot: symlinked}).Run(context.Background(), valid); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("symlinked evidence tree: %v", err)
	}
	assertNoLaunches(t, runtimeRoot)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Runner{RuntimeRoot: runtimeRoot}).Run(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled before start: %v", err)
	}
}

func TestRunnerDoesNotInheritDaemonEnvironment(t *testing.T) {
	t.Setenv("SWITCHYARD_TEST_LEAK", "leaked")
	runtimeRoot := privateTempDir(t)
	var stdout, stderr bytes.Buffer
	outcome, err := Runner{RuntimeRoot: runtimeRoot}.Run(context.Background(), testSpec(t, &stdout, &stderr, `printf '%s' "${SWITCHYARD_TEST_LEAK:-clean}"`))
	if err != nil || outcome.ExitCode != 0 || stdout.String() != "clean" {
		t.Fatalf("outcome=%+v err=%v stdout=%q", outcome, err, stdout.String())
	}
}

const (
	crashedDaemonModeVariable   = "SWITCHYARD_TEST_CRASHED_DAEMON_MODE"
	crashedDaemonRunVariable    = "SWITCHYARD_TEST_CRASHED_DAEMON_RUN"
	crashedDaemonScopeVariable  = "SWITCHYARD_TEST_CRASHED_DAEMON_SCOPE"
	crashedDaemonScriptVariable = "SWITCHYARD_TEST_CRASHED_DAEMON_SCRIPT"
)

// TestCrashedDaemonHelper is not a test: re-invoked as a subprocess it plays
// a daemon that launched a finite preparation and then died hard without
// stopping it, exactly as a crash during an install would.
func TestCrashedDaemonHelper(t *testing.T) {
	mode := os.Getenv(crashedDaemonModeVariable)
	if mode == "" {
		t.Skip("helper mode only")
	}
	launchDirectory := os.Getenv(crashedDaemonRunVariable)
	host := processhost.New(processhost.Config{LeaderSettleDelay: 50 * time.Millisecond})
	_, err := host.Start(context.Background(), processhost.LaunchSpec{
		EnvironmentID: os.Getenv(crashedDaemonScopeVariable), ServiceID: "install", RunID: filepath.Base(launchDirectory),
		Executable: "/bin/sh", Arguments: []string{"-c", os.Getenv(crashedDaemonScriptVariable)},
		Directory: launchDirectory, RunDirectory: launchDirectory, Environment: []string{"PATH=/usr/bin:/bin"},
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownershipPath := filepath.Join(launchDirectory, processhost.OwnershipFileName)
	switch mode {
	case "finished":
		if _, err := host.WaitExit(context.Background(), ownershipPath); err != nil {
			t.Fatal(err)
		}
	case "refreshed":
		// The daemon's periodic membership refresh persisted the leader's
		// child before the leader exited; then the daemon died with the
		// leader exited but its group still populated.
		time.Sleep(200 * time.Millisecond)
		if _, err := host.Reconcile(context.Background(), ownershipPath); err != nil {
			t.Fatal(err)
		}
		if err := host.WaitExited(context.Background(), ownershipPath); err != nil {
			t.Fatal(err)
		}
	default:
		// Outlive the settle delay so the re-executed /bin/sh leader's
		// fingerprint is persisted, as any daemon running for more than a
		// second would have.
		time.Sleep(300 * time.Millisecond)
	}
	os.Exit(0)
}

func crashDaemonWith(t *testing.T, launchDirectory, scope, script, mode string) processhost.Ownership {
	t.Helper()
	if err := os.Mkdir(launchDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestCrashedDaemonHelper$")
	helper.Env = append(os.Environ(),
		crashedDaemonModeVariable+"="+mode, crashedDaemonRunVariable+"="+launchDirectory,
		crashedDaemonScopeVariable+"="+scope, crashedDaemonScriptVariable+"="+script,
	)
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("crashed daemon helper: %v\n%s", err, output)
	}
	ownership, err := processhost.LoadOwnership(filepath.Join(launchDirectory, processhost.OwnershipFileName))
	if err != nil {
		t.Fatal(err)
	}
	return ownership
}

func TestRecoverInterruptedRunsStopsOnlyVerifiedRunningGroups(t *testing.T) {
	runtimeRoot := privateTempDir(t)
	runsRoot := filepath.Join(runtimeRoot, DirectoryName)
	if err := os.Mkdir(runsRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	// A preparation the previous daemon left running: a hard crash while
	// the leader runs.
	orphanDirectory := filepath.Join(runsRoot, "launch_orphan")
	orphan := crashDaemonWith(t, orphanDirectory, OwnerScope, "sleep 30", "running")

	// A leader that exited before the crash but left a straggler in its
	// group. The running daemon had persisted the child as a member, so the
	// group is still positively owned through the record.
	stragglerDirectory := filepath.Join(runsRoot, "launch_straggler")
	stragglerScript := `sleep 30 & echo $! > "` + stragglerDirectory + `/child.pid"; sleep 0.5; exit 0`
	straggler := crashDaemonWith(t, stragglerDirectory, OwnerScope, stragglerScript, "refreshed")
	stragglerPID := readPID(t, filepath.Join(stragglerDirectory, "child.pid"))
	if len(straggler.Members) < 2 {
		t.Fatalf("straggler membership was not persisted: %+v", straggler)
	}

	// The same shape without a persisted member: the child cannot be tied
	// to a verified ancestor once the leader is gone, so a restarted daemon
	// reports it and never signals it.
	detachedDirectory := filepath.Join(runsRoot, "launch_detached")
	_ = crashDaemonWith(t, detachedDirectory, OwnerScope,
		`sleep 30 & echo $! > "`+detachedDirectory+`/child.pid"; exit 0`, "running")
	detachedPID := readPID(t, filepath.Join(detachedDirectory, "child.pid"))

	// A leader that exited and was reaped before the crash could record a
	// stop.
	finishedDirectory := filepath.Join(runsRoot, "launch_finished")
	finished := crashDaemonWith(t, finishedDirectory, OwnerScope, "exit 0", "finished")
	if finished.State != "running" || finished.Exit == nil {
		t.Fatalf("finished record: %+v", finished)
	}

	// A record whose persisted start time no longer matches the live
	// leader: the PID could belong to anyone, so it must be left alone.
	driftedDirectory := filepath.Join(runsRoot, "launch_drifted")
	drifted := crashDaemonWith(t, driftedDirectory, OwnerScope, "sleep 30", "running")
	rewriteOwnership(t, driftedDirectory, func(ownership *processhost.Ownership) {
		ownership.Leader.StartedAt = ownership.Leader.StartedAt.Add(time.Second)
		ownership.Members[0].StartedAt = ownership.Leader.StartedAt
	})

	// Process evidence that is not a finite preparation is never this
	// recovery's to act on.
	foreignDirectory := filepath.Join(runsRoot, "launch_foreign")
	foreign := crashDaemonWith(t, foreignDirectory, "env_live", "sleep 30", "running")

	// A crash between fork and verification leaves only an intent.
	intentDirectory := filepath.Join(runsRoot, "launch_intent")
	if err := os.Mkdir(intentDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(intentDirectory, processhost.LaunchIntentFileName), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A malformed record is reported, never interpreted.
	malformedDirectory := filepath.Join(runsRoot, "launch_malformed")
	if err := os.Mkdir(malformedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformedDirectory, processhost.OwnershipFileName), []byte("{\"schemaVersion\":1"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A launch directory that is not private is not consulted at all, and a
	// foreign file in the tree is never touched.
	exposedDirectory := filepath.Join(runsRoot, "launch_exposed")
	if err := os.Mkdir(exposedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exposedDirectory, processhost.OwnershipFileName), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreignFile := filepath.Join(runsRoot, "notes.txt")
	if err := os.WriteFile(foreignFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		for _, pid := range []int{orphan.Leader.PID, drifted.Leader.PID, foreign.Leader.PID, stragglerPID, detachedPID} {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})

	restarted := NewProcessHost()
	report, err := RecoverInterruptedRuns(context.Background(), restarted, runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stopped != 3 || report.Unverified != 4 || report.Truncated {
		t.Fatalf("report: %+v", report)
	}
	awaitGone(t, orphan.Leader.PID, "orphaned preparation leader")
	awaitGone(t, stragglerPID, "persisted straggling descendant")
	for name, pid := range map[string]int{"drifted": drifted.Leader.PID, "foreign": foreign.Leader.PID, "detached child": detachedPID} {
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("%s leader was signalled without verified ownership: %v", name, err)
		}
	}
	// Verified launches leave no evidence behind; the straggler's pid file
	// is not the host's and keeps its directory in place.
	for _, directory := range []string{orphanDirectory, finishedDirectory} {
		if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: verified launch evidence survived: %v", directory, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(stragglerDirectory, "child.pid")); err != nil {
		t.Fatalf("foreign file inside a launch directory was removed: %v", err)
	}
	// Unverifiable evidence and foreign files are left exactly as found.
	for _, directory := range []string{driftedDirectory, detachedDirectory, foreignDirectory, intentDirectory, malformedDirectory, exposedDirectory} {
		if _, err := os.Lstat(directory); err != nil {
			t.Fatalf("%s: unverified evidence was removed: %v", directory, err)
		}
	}
	if contents, err := os.ReadFile(foreignFile); err != nil || string(contents) != "keep" {
		t.Fatalf("foreign file: %q %v", contents, err)
	}
	// Recovery is idempotent: nothing is left to stop and nothing new is
	// reported as verified.
	report, err = RecoverInterruptedRuns(context.Background(), restarted, runtimeRoot)
	if err != nil || report.Stopped != 0 || report.Unverified != 4 {
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
	if _, err := RecoverInterruptedRuns(context.Background(), NewProcessHost(), "relative"); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("relative root: %v", err)
	}
	exposed := t.TempDir()
	if err := os.Chmod(exposed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(exposed, DirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverInterruptedRuns(context.Background(), NewProcessHost(), exposed); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("group-readable runtime root: %v", err)
	}
	symlinked := privateTempDir(t)
	if err := os.Symlink(runtimeRoot, filepath.Join(symlinked, DirectoryName)); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverInterruptedRuns(context.Background(), NewProcessHost(), symlinked); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("symlinked evidence tree: %v", err)
	}
}

func testSpec(t *testing.T, stdout, stderr *bytes.Buffer, script string) Spec {
	t.Helper()
	home := t.TempDir()
	return Spec{
		ID: "install", Executable: "/bin/sh", Arguments: []string{"-c", script},
		Environment: []string{"HOME=" + home, "PATH=/usr/bin:/bin", "TMPDIR=" + home},
		Directory:   home, Stdout: stdout, Stderr: stderr, Timeout: 10 * time.Second,
	}
}

func assertNoLaunches(t *testing.T, runtimeRoot string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(runtimeRoot, DirectoryName))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) != 0 {
		t.Fatalf("launch evidence survived verified finishes: %s", strings.Join(names, ", "))
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

func rewriteOwnership(t *testing.T, launchDirectory string, mutate func(*processhost.Ownership)) {
	t.Helper()
	path := filepath.Join(launchDirectory, processhost.OwnershipFileName)
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

// awaitGone waits until pid no longer names a process.
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
