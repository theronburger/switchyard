package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/theronburger/switchyard/internal/runtime/finiterun"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
	"github.com/theronburger/switchyard/internal/state"
)

const crashedPreparationDaemonRunVariable = "SWITCHYARD_TEST_CRASHED_PREPARATION_RUN"

// TestCrashedPreparationDaemonHelper is not a test: re-invoked as a
// subprocess it plays a daemon that launched a workspace preparation step or
// environment initialization command and died without stopping it.
func TestCrashedPreparationDaemonHelper(t *testing.T) {
	launchDirectory := os.Getenv(crashedPreparationDaemonRunVariable)
	if launchDirectory == "" {
		t.Skip("helper mode only")
	}
	host := processhost.New(processhost.Config{LeaderSettleDelay: 50 * time.Millisecond})
	if _, err := host.Start(context.Background(), processhost.LaunchSpec{
		EnvironmentID: finiterun.OwnerScope, ServiceID: "install", RunID: filepath.Base(launchDirectory),
		Executable: "/bin/sh", Arguments: []string{"-c", "sleep 30"},
		Directory: launchDirectory, RunDirectory: launchDirectory, Environment: []string{"PATH=/usr/bin:/bin"},
		Stdout: io.Discard, Stderr: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	// Outlive the settle delay so the re-executed /bin/sh leader's settled
	// fingerprint is persisted, as any daemon that ran for more than a
	// second would have done.
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
}

// TestBootStopsPreparationGroupOrphanedByThePreviousDaemon proves that a
// daemon restart does not orphan an install or initialization command's
// process group: boot stops it through its persisted, positively verified
// ownership before any profile exists, and the verified launch leaves no
// evidence behind.
func TestBootStopsPreparationGroupOrphanedByThePreviousDaemon(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	launchDirectory := filepath.Join(root, "runtime", finiterun.DirectoryName, "launch_01")
	if err := os.MkdirAll(launchDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestCrashedPreparationDaemonHelper$")
	helper.Env = append(os.Environ(), crashedPreparationDaemonRunVariable+"="+launchDirectory)
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("crashed daemon helper: %v\n%s", err, output)
	}
	ownershipPath := filepath.Join(launchDirectory, processhost.OwnershipFileName)
	orphan, err := processhost.LoadOwnership(ownershipPath)
	if err != nil || orphan.State != "running" {
		t.Fatalf("orphaned ownership: %+v err=%v", orphan, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-orphan.Leader.PID, syscall.SIGKILL) })
	if err := syscall.Kill(orphan.Leader.PID, 0); err != nil {
		t.Fatalf("the crashed daemon's preparation is not running: %v", err)
	}

	store, err := state.Open(ctx, state.Config{Path: filepath.Join(root, "state.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	paths := applicationPaths{root: root, directory: root, database: filepath.Join(root, "state.sqlite"), configuration: filepath.Join(root, "configuration.yaml")}
	if _, err := buildConfiguredProfileRuntime(ctx, store, paths, "daemon_02", repositoryInventory{}, func() {}); err != nil {
		t.Fatalf("boot without a profile: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(orphan.Leader.PID, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("orphaned preparation leader %d survived the daemon restart", orphan.Leader.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Lstat(launchDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified launch evidence survived the restart: %v", err)
	}
}
