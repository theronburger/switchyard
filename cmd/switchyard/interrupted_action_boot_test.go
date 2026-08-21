package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	actioncontrol "github.com/theronburger/switchyard/internal/control/action"
	"github.com/theronburger/switchyard/internal/runtime/processhost"
	"github.com/theronburger/switchyard/internal/state"
)

const (
	crashedActionDaemonRunVariable = "SWITCHYARD_TEST_CRASHED_ACTION_RUN"
)

// TestCrashedActionDaemonHelper is not a test: re-invoked as a subprocess it
// plays a daemon that launched a finite action and died without stopping it.
func TestCrashedActionDaemonHelper(t *testing.T) {
	runDirectory := os.Getenv(crashedActionDaemonRunVariable)
	if runDirectory == "" {
		t.Skip("helper mode only")
	}
	host := processhost.New(processhost.Config{LeaderSettleDelay: 50 * time.Millisecond})
	if _, err := host.Start(context.Background(), processhost.LaunchSpec{
		EnvironmentID: actioncontrol.OwnerScope, ServiceID: "tidy", RunID: filepath.Base(runDirectory),
		Executable: "/bin/sh", Arguments: []string{"-c", "sleep 30"},
		Directory: runDirectory, RunDirectory: runDirectory, Environment: []string{"PATH=/usr/bin:/bin"},
	}); err != nil {
		t.Fatal(err)
	}
	// Outlive the settle delay so the re-executed /bin/sh leader's settled
	// fingerprint is persisted, as any daemon that ran for more than a
	// second would have done.
	time.Sleep(300 * time.Millisecond)
	os.Exit(0)
}

// TestBootStopsActionGroupOrphanedByThePreviousDaemon proves that a daemon
// restart does not orphan a finite action's process group: boot stops it
// through its persisted, positively verified ownership before any profile
// exists, and the interrupted operation is failed as on every boot.
func TestBootStopsActionGroupOrphanedByThePreviousDaemon(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runDirectory := filepath.Join(root, "runtime", "actions", "sample", "operation_01")
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestCrashedActionDaemonHelper$")
	helper.Env = append(os.Environ(), crashedActionDaemonRunVariable+"="+runDirectory)
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("crashed daemon helper: %v\n%s", err, output)
	}
	ownershipPath := filepath.Join(runDirectory, processhost.OwnershipFileName)
	orphan, err := processhost.LoadOwnership(ownershipPath)
	if err != nil || orphan.State != "running" {
		t.Fatalf("orphaned ownership: %+v err=%v", orphan, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-orphan.Leader.PID, syscall.SIGKILL) })
	if err := syscall.Kill(orphan.Leader.PID, 0); err != nil {
		t.Fatalf("the crashed daemon's action is not running: %v", err)
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
			t.Fatalf("orphaned action leader %d survived the daemon restart", orphan.Leader.PID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	recovered, err := processhost.LoadOwnership(ownershipPath)
	if err != nil || recovered.State != "stopped" {
		t.Fatalf("recovered ownership: %+v err=%v", recovered, err)
	}
}
