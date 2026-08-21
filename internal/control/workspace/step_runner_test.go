package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/control/cleanup"
	"github.com/theronburger/switchyard/internal/runtime/finiterun"
)

func TestExactStepRunnerWritesOnlyPrivateBoundedLogs(t *testing.T) {
	runtimeRoot := privateRuntimeRoot(t)
	working := t.TempDir()
	runDirectory := filepath.Join(runtimeRoot, "repositories", "sample", "run")
	runner := ExactStepRunner{RuntimeRoot: runtimeRoot}
	err := runner.Run(context.Background(), StepSpec{
		ID: "print", Executable: "/bin/echo", Arguments: []string{"hello"},
		Environment: []string{"HOME=/tmp", "PATH=/usr/bin:/bin", "TMPDIR=/tmp"},
		Directory:   working, RunDirectory: runDirectory, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(runDirectory, "stdout.log"))
	if err != nil || string(contents) != "hello\n" {
		t.Fatalf("stdout=%q err=%v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(working, "stdout.log")); !os.IsNotExist(err) {
		t.Fatalf("log leaked into worktree: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(runDirectory, ownershipMarkerFilename))
	if err != nil || !strings.Contains(string(marker), `"kind":"preparation-step"`) {
		t.Fatalf("ownership marker=%q err=%v", marker, err)
	}
}

func TestExactStepRunnerRejectsSymlinkedPrivatePath(t *testing.T) {
	runtimeRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(runtimeRoot, "repositories")); err != nil {
		t.Fatal(err)
	}
	err := (ExactStepRunner{RuntimeRoot: runtimeRoot}).Run(context.Background(), StepSpec{
		ID: "print", Executable: "/bin/echo", Arguments: []string{"hello"},
		Environment: []string{"HOME=/tmp", "PATH=/usr/bin:/bin", "TMPDIR=/tmp"},
		Directory:   t.TempDir(), RunDirectory: filepath.Join(runtimeRoot, "repositories", "sample"), Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("symlinked private run path was accepted")
	}
}

// privateRuntimeRoot mirrors the daemon's owner-only runtime directory; the
// per-test temporary directory itself is world-readable.
func privateRuntimeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestExactStepRunnerKeepsProcessEvidenceOutOfTheStepDirectory(t *testing.T) {
	runtimeRoot := privateRuntimeRoot(t)
	working := t.TempDir()
	fingerprint := strings.Repeat("ab", 32)
	runDirectory := filepath.Join(runtimeRoot, "repositories", "sample", "worktree_01", "preparation", fingerprint, "install")
	runner := ExactStepRunner{RuntimeRoot: runtimeRoot}
	step := StepSpec{
		ID: "install", Executable: "/bin/sh", Arguments: []string{"-c", `sleep 30 & echo $! > "$HOME/child.pid"; echo done`},
		Environment: []string{"HOME=" + working, "PATH=/usr/bin:/bin", "TMPDIR=" + working},
		Directory:   working, RunDirectory: runDirectory, Timeout: 10 * time.Second,
	}
	if err := runner.Run(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	// A descendant that outlived the leader was stopped through the owned
	// group, not left behind.
	awaitStepProcessGone(t, readStepPID(t, filepath.Join(working, "child.pid")))

	// The step directory holds exactly what the private-preparation cleanup
	// planner positively identifies: its marker and bounded logs.
	entries, err := os.ReadDir(runDirectory)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != "ownership.json,stderr.log,stdout.log" {
		t.Fatalf("step directory entries: %v", names)
	}
	inventory, err := cleanup.PrivatePreparationPlanner{RuntimeRoot: runtimeRoot, CurrentFingerprints: map[string]string{}}.
		Inventory(context.Background(), cleanup.Scope{Kind: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Candidates) != 1 || len(inventory.Protected) != 0 || inventory.Candidates[0].Fingerprint != fingerprint {
		t.Fatalf("cleanup inventory after a run: %+v", inventory)
	}
	// A verified finish leaves no process evidence at all; the evidence
	// tree is the runner's, never the cleanup planner's.
	launches, err := os.ReadDir(filepath.Join(runtimeRoot, finiterun.DirectoryName))
	if err != nil || len(launches) != 0 {
		t.Fatalf("launch evidence after a verified finish: %v err=%v", launches, err)
	}

	// Re-running into the same step directory, as a retry after failure
	// does, reuses the marker and truncates the logs.
	if err := runner.Run(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(runDirectory, "stdout.log"))
	if err != nil || string(contents) != "done\n" {
		t.Fatalf("stdout after rerun=%q err=%v", contents, err)
	}
}

func TestExactStepRunnerMapsFailuresAndTimeouts(t *testing.T) {
	runtimeRoot := privateRuntimeRoot(t)
	working := t.TempDir()
	runner := ExactStepRunner{RuntimeRoot: runtimeRoot}
	environment := []string{"HOME=" + working, "PATH=/usr/bin:/bin", "TMPDIR=" + working}

	err := runner.Run(context.Background(), StepSpec{
		ID: "fail", Executable: "/bin/sh", Arguments: []string{"-c", "exit 3"}, Environment: environment,
		Directory: working, RunDirectory: filepath.Join(runtimeRoot, "repositories", "sample", "fail"), Timeout: time.Second,
	})
	if !errors.Is(err, ErrStepFailed) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("non-zero exit: %v", err)
	}

	err = runner.Run(context.Background(), StepSpec{
		ID: "slow", Executable: "/bin/sh", Arguments: []string{"-c", `echo $$ > "$HOME/leader.pid"; sleep 30`}, Environment: environment,
		Directory: working, RunDirectory: filepath.Join(runtimeRoot, "repositories", "sample", "slow"), Timeout: 300 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrStepFailed) {
		t.Fatalf("timeout: %v", err)
	}
	awaitStepProcessGone(t, readStepPID(t, filepath.Join(working, "leader.pid")))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = runner.Run(ctx, StepSpec{
		ID: "cancelled", Executable: "/bin/echo", Environment: environment,
		Directory: working, RunDirectory: filepath.Join(runtimeRoot, "repositories", "sample", "cancelled"), Timeout: time.Second,
	})
	if !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("cancelled before start: %v", err)
	}
	launches, err := os.ReadDir(filepath.Join(runtimeRoot, finiterun.DirectoryName))
	if err != nil || len(launches) != 0 {
		t.Fatalf("launch evidence after verified finishes: %v err=%v", launches, err)
	}
}

func readStepPID(t *testing.T, path string) int {
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

func awaitStepProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("process %d outlived its owned step", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
