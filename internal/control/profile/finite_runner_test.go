package profile

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

	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
	"github.com/theronburger/switchyard/internal/runtime/finiterun"
)

func TestFiniteRunnerOwnsTheWholeGroupAndKeepsLogsInTheRunDirectory(t *testing.T) {
	runtimeRoot := privateFiniteRoot(t)
	working := t.TempDir()
	runDirectory := filepath.Join(runtimeRoot, "repositories", "sample", "wt", "environments", "env", "runs", "run_01", "preparations", "api", "migrate")
	step := finiteStep(working, runDirectory, `sleep 30 & echo $! > "$HOME/child.pid"; echo migrated`)
	if err := (FiniteRunner{RuntimeRoot: runtimeRoot}).Run(context.Background(), step); err != nil {
		t.Fatal(err)
	}
	awaitFiniteProcessGone(t, readFinitePID(t, filepath.Join(working, "child.pid")))
	contents, err := os.ReadFile(filepath.Join(runDirectory, "stdout.log"))
	if err != nil || string(contents) != "migrated\n" {
		t.Fatalf("stdout=%q err=%v", contents, err)
	}
	entries, err := os.ReadDir(runDirectory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("run directory entries: %v err=%v", entries, err)
	}
	if launches, err := os.ReadDir(filepath.Join(runtimeRoot, finiterun.DirectoryName)); err != nil || len(launches) != 0 {
		t.Fatalf("launch evidence after a verified finish: %v err=%v", launches, err)
	}
}

func TestFiniteRunnerMapsFailuresTimeoutsAndInvalidRoots(t *testing.T) {
	runtimeRoot := privateFiniteRoot(t)
	working := t.TempDir()
	runner := FiniteRunner{RuntimeRoot: runtimeRoot}

	err := runner.Run(context.Background(), finiteStep(working, filepath.Join(runtimeRoot, "fail"), "exit 2"))
	if !errors.Is(err, ErrFiniteCommandFailed) {
		t.Fatalf("non-zero exit: %v", err)
	}

	slow := finiteStep(working, filepath.Join(runtimeRoot, "slow"), `echo $$ > "$HOME/leader.pid"; sleep 30`)
	slow.Timeout = 300 * time.Millisecond
	if err := runner.Run(context.Background(), slow); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	awaitFiniteProcessGone(t, readFinitePID(t, filepath.Join(working, "leader.pid")))

	invalid := finiteStep(working, filepath.Join(runtimeRoot, "invalid"), "exit 0")
	invalid.Environment = []string{"PATH=/usr/bin:/bin"}
	if err := runner.Run(context.Background(), invalid); !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("missing required environment: %v", err)
	}

	exposed := t.TempDir()
	if err := os.Chmod(exposed, 0o755); err != nil {
		t.Fatal(err)
	}
	err = (FiniteRunner{RuntimeRoot: exposed}).Run(context.Background(), finiteStep(working, filepath.Join(exposed, "run"), "exit 0"))
	if !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("exposed runtime root: %v", err)
	}
	if launches, err := os.ReadDir(filepath.Join(runtimeRoot, finiterun.DirectoryName)); err != nil || len(launches) != 0 {
		t.Fatalf("launch evidence after verified finishes: %v err=%v", launches, err)
	}
}

func finiteStep(working, runDirectory, script string) environmentcontrol.PreparationSpec {
	return environmentcontrol.PreparationSpec{
		ID: "api.migrate", ServiceID: "api", Executable: "/bin/sh", Arguments: []string{"-c", script},
		Environment: []string{"HOME=" + working, "PATH=/usr/bin:/bin", "TMPDIR=" + working},
		Directory:   working, RunDirectory: runDirectory, Timeout: 10 * time.Second,
	}
}

func privateFiniteRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func readFinitePID(t *testing.T, path string) int {
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

func awaitFiniteProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("process %d outlived its owned command", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
