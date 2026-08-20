package processhost

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
)

// shellLaunchSpec launches macOS /bin/sh, which replaces its own executable
// image after start, so the leader fingerprint recorded at launch drifts.
func shellLaunchSpec(runDirectory, script string) LaunchSpec {
	return LaunchSpec{
		EnvironmentID: "env_test", ServiceID: "service_test", RunID: "run_shell",
		Executable: "/bin/sh", Arguments: []string{"-c", script},
		Environment: []string{"PATH=/usr/bin:/bin", "HOME=" + runDirectory},
		Directory:   runDirectory, RunDirectory: runDirectory,
	}
}

func privateRunDirectory(t *testing.T) string {
	t.Helper()
	runDirectory := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	return runDirectory
}

func TestOwnUnreapedLeaderIsRequalifiedAndStoppable(t *testing.T) {
	runDirectory := privateRunDirectory(t)
	host := New(Config{GracePeriod: time.Second, KillWait: time.Second, PollInterval: 10 * time.Millisecond, LeaderSettleDelay: 300 * time.Millisecond})
	ownership, err := host.Start(context.Background(), shellLaunchSpec(runDirectory, "sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	ownershipPath := filepath.Join(runDirectory, OwnershipFileName)
	t.Cleanup(func() { stopOwnedRun(host, ownershipPath) })

	// A host that is not the leader's parent must keep refusing the drift.
	stranger := New(Config{GracePeriod: time.Second, KillWait: time.Second, PollInterval: 10 * time.Millisecond})
	if _, err := stranger.Stop(context.Background(), ownershipPath); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("stranger stop of a re-executed leader: %v", err)
	}
	if err := syscall.Kill(ownership.Leader.PID, 0); err != nil {
		t.Fatalf("stranger signalled the leader: %v", err)
	}

	// The parent settles the leader's real fingerprint shortly after start.
	deadline := time.Now().Add(2 * time.Second)
	for {
		settled, err := LoadOwnership(ownershipPath)
		if err != nil {
			t.Fatal(err)
		}
		if settled.Leader.CommandFingerprint != ownership.Leader.CommandFingerprint {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("own leader fingerprint was never requalified")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Once persisted, even a stranger can verify and stop it; here the
	// parent does so and records the exit.
	observation, err := host.Stop(context.Background(), ownershipPath)
	if err != nil || observation.State != "stopped" || observation.MemberCount != 0 {
		t.Fatalf("own stop: %+v err=%v", observation, err)
	}
	exit, err := host.WaitExit(context.Background(), ownershipPath)
	if err != nil || exit.Signal != int(syscall.SIGTERM) {
		t.Fatalf("exit: %+v err=%v", exit, err)
	}
}

func TestDeferredReapLetsTheParentStopStragglersAfterTheLeaderExits(t *testing.T) {
	runDirectory := privateRunDirectory(t)
	host := New(Config{GracePeriod: time.Second, KillWait: time.Second, PollInterval: 10 * time.Millisecond})
	spec := shellLaunchSpec(runDirectory, `sleep 30 & echo $! > "$HOME/child.pid"; exit 7`)
	spec.DeferReap = true
	ownership, err := host.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ownershipPath := filepath.Join(runDirectory, OwnershipFileName)
	t.Cleanup(func() { collectRun(host, ownershipPath) })
	if err := host.WaitExited(context.Background(), ownershipPath); err != nil {
		t.Fatal(err)
	}
	// The leader is a zombie this host has deliberately not reaped, so its
	// PID still exists.
	if err := syscall.Kill(ownership.Leader.PID, 0); err != nil {
		t.Fatalf("exited leader was reaped before the caller allowed it: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(runDirectory, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	straggler, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil {
		t.Fatal(err)
	}
	// The straggler was reparented away from the exited leader; only the
	// unreaped-child rule lets the parent act on it.
	observation, err := host.Stop(context.Background(), ownershipPath)
	if err != nil || observation.State != "stopped" {
		t.Fatalf("stop after leader exit: %+v err=%v", observation, err)
	}
	if err := syscall.Kill(straggler, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("straggler survived: %v", err)
	}
	exit, err := host.WaitExit(context.Background(), ownershipPath)
	if err != nil || exit.ExitCode != 7 {
		t.Fatalf("exit: %+v err=%v", exit, err)
	}
	if _, err := host.WaitExit(context.Background(), ownershipPath); err != nil {
		t.Fatalf("repeated WaitExit: %v", err)
	}
	host.Forget(ownershipPath)
	if _, err := host.WaitExit(context.Background(), ownershipPath); !errors.Is(err, ErrExitNotObservable) {
		t.Fatalf("WaitExit after Forget: %v", err)
	}
	if _, err := stranger().WaitExit(context.Background(), ownershipPath); !errors.Is(err, ErrExitNotObservable) {
		t.Fatalf("stranger WaitExit: %v", err)
	}
}

func TestStrangerStopKeepsTheStrictRuleForReparentedDescendants(t *testing.T) {
	runDirectory := privateRunDirectory(t)
	host := New(Config{GracePeriod: time.Second, KillWait: time.Second, PollInterval: 10 * time.Millisecond})
	spec := shellLaunchSpec(runDirectory, `sleep 30 & echo $! > "$HOME/child.pid"; exit 0`)
	spec.DeferReap = true
	_, err := host.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ownershipPath := filepath.Join(runDirectory, OwnershipFileName)
	t.Cleanup(func() {
		collectRun(host, ownershipPath)
		if contents, err := os.ReadFile(filepath.Join(runDirectory, "child.pid")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(contents))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	if err := host.WaitExited(context.Background(), ownershipPath); err != nil {
		t.Fatal(err)
	}
	if _, err := stranger().Stop(context.Background(), ownershipPath); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("stranger stop with a reparented descendant: %v", err)
	}
}

func stranger() *Host {
	return New(Config{GracePeriod: time.Second, KillWait: time.Second, PollInterval: 10 * time.Millisecond})
}

// collectRun stops whatever is left, lets the host reap and record the
// leader, and forgets the run so no write races the temporary directory's
// removal.
func collectRun(host *Host, ownershipPath string) {
	stopOwnedRun(host, ownershipPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = host.WaitExit(ctx, ownershipPath)
	host.Forget(ownershipPath)
}
