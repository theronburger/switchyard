// Package finiterun gives the daemon's finite preparation commands (workspace
// preparation steps and environment initialization commands) the same
// crash-durable, positively verified process ownership that services and
// profile actions have. Every launch gets its own private evidence directory
// under the runtime root, where the process host persists the launch intent
// before fork and the verified leader identity immediately after, so a daemon
// that dies mid-command leaves evidence a restarted daemon can act on with
// positive verification rather than an unowned process group.
//
// The evidence tree is deliberately separate from the callers' own run
// directories: a workspace step directory carries the private-preparation
// cleanup marker and bounded logs, and its layout is what the cleanup planner
// positively identifies, so process records never appear there.
package finiterun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

const (
	// DirectoryName is the directory under the runtime root that holds one
	// evidence directory per finite preparation launch.
	DirectoryName = "preparation-runs"
	// OwnerScope is the value finite preparation ownership records carry in
	// the environment slot of the shared process-ownership schema; the
	// service slot carries the caller's step identifier and the run slot the
	// unique launch identifier. Recovery matches on persisted PID, process
	// group, start time, and command fingerprint, never on this label or on
	// an executable name.
	OwnerScope = "finite-preparation"

	terminationGrace = 2 * time.Second
	killWait         = 2 * time.Second
	// stopBudget bounds the verified TERM, grace, re-verify, KILL sequence
	// that finishes a timed-out, cancelled, or straggling group.
	stopBudget = terminationGrace + killWait + 10*time.Second
	// membershipRefreshInterval paces the ownership refresh of a running
	// command so recovery after a crash sees recent leader and member
	// identity.
	membershipRefreshInterval = 15 * time.Second
	// MaximumTimeout bounds a single finite preparation command.
	MaximumTimeout = 30 * time.Minute
)

var (
	ErrInvalidSpec = errors.New("finite preparation command is invalid")
	ErrInvalidRoot = errors.New("finite preparation runtime root is not an owned private directory")
	ErrStart       = errors.New("finite preparation command could not start")
	// ErrGroupUnverified reports that the command's process group no longer
	// matched its persisted ownership, so it was reported rather than
	// signalled and its evidence was left in place.
	ErrGroupUnverified = errors.New("finite preparation process group could not be positively verified")
)

// Spec is a fully compiled executable invocation. Callers own the bounded
// log writers and have already proven the executable, directory, and
// environment; the runner re-checks the shape it depends on.
type Spec struct {
	// ID labels the command in its ownership record (a workspace step ID or
	// an environment preparation ID). It is never matched against processes.
	ID          string
	Executable  string
	Arguments   []string
	Environment []string
	Directory   string
	Stdout      io.Writer
	Stderr      io.Writer
	Timeout     time.Duration
}

// Outcome is the bounded result of one command whose group is fully stopped.
type Outcome struct {
	ExitCode int
	Signal   int
	TimedOut bool
}

// Runner executes a finite command in a positively owned process group whose
// evidence lives under RuntimeRoot/preparation-runs/<launch>. A command ends
// with its whole group stopped through that ownership, including descendants
// that outlived the leader; a cleanly finished launch's evidence is then
// removed so the flat tree only ever holds launches that still need recovery
// or that could not be verified.
type Runner struct {
	RuntimeRoot string
	// Processes owns launch, verification, and signalling. When nil a host
	// with the preparation termination grace is used.
	Processes *processhost.Host
	// Random overrides the launch identifier entropy; nil uses crypto/rand.
	Random io.Reader
}

// NewProcessHost returns the host a daemon should share between its finite
// preparation runners and RecoverInterruptedRuns.
func NewProcessHost() *processhost.Host {
	return processhost.New(processhost.Config{GracePeriod: terminationGrace, KillWait: killWait})
}

// Run executes the command and reports its exit. A non-zero exit status is
// reported through Outcome, not through the error; errors are reserved for
// invalid commands, start failures, context cancellation, and an unverifiable
// group.
func (runner Runner) Run(ctx context.Context, spec Spec) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := validateSpec(spec); err != nil {
		return Outcome{}, err
	}
	launchDirectory, err := runner.createLaunchDirectory()
	if err != nil {
		return Outcome{}, err
	}
	host := runner.host()
	ownershipPath := filepath.Join(launchDirectory, processhost.OwnershipFileName)
	defer host.Forget(ownershipPath)
	runContext, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	if _, err := host.Start(runContext, processhost.LaunchSpec{
		EnvironmentID: OwnerScope, ServiceID: spec.ID, RunID: filepath.Base(launchDirectory),
		Executable: spec.Executable, Arguments: spec.Arguments, Environment: spec.Environment,
		Directory: spec.Directory, RunDirectory: launchDirectory,
		Stdout: spec.Stdout, Stderr: spec.Stderr, DeferReap: true,
	}); err != nil {
		// A failed start cleared its own intent, and a start interrupted
		// after verification stopped its group; neither leaves anything a
		// restart must act on. Evidence in any other state is kept for it.
		removeFinishedLaunch(launchDirectory)
		switch {
		case errors.Is(err, processhost.ErrAlreadyOwned), errors.Is(err, processhost.ErrOrphanUnverified):
			return Outcome{}, ErrInvalidSpec
		case runContext.Err() != nil:
			return Outcome{}, runContext.Err()
		default:
			return Outcome{}, ErrStart
		}
	}

	waitErr := awaitExit(runContext, host, ownershipPath)
	interrupted := waitErr != nil && runContext.Err() != nil
	if waitErr != nil && !interrupted {
		return Outcome{}, waitErr
	}
	// The leader is exited-but-unreaped (or still running on interruption),
	// so its group ID still positively denotes this launch while descendants
	// that outlived it are stopped. Only then is the leader reaped.
	stopContext, cancelStop := context.WithTimeout(context.Background(), stopBudget)
	defer cancelStop()
	if _, err := host.Stop(stopContext, ownershipPath); err != nil {
		if errors.Is(err, processhost.ErrOwnershipMismatch) || errors.Is(err, processhost.ErrUnstableGroup) {
			return Outcome{}, fmt.Errorf("%w: %v", ErrGroupUnverified, err)
		}
		return Outcome{}, err
	}
	exit, err := host.WaitExit(stopContext, ownershipPath)
	if err != nil {
		return Outcome{}, err
	}
	host.Forget(ownershipPath)
	removeFinishedLaunch(launchDirectory)
	if interrupted && !errors.Is(runContext.Err(), context.DeadlineExceeded) {
		return Outcome{}, runContext.Err()
	}
	return Outcome{ExitCode: exit.ExitCode, Signal: exit.Signal, TimedOut: interrupted}, nil
}

// awaitExit waits for the leader while periodically re-reading the owned
// group, so the persisted leader fingerprint and membership a restart would
// recover from stay current for long-running commands.
func awaitExit(ctx context.Context, host *processhost.Host, ownershipPath string) error {
	for {
		window, cancel := context.WithTimeout(ctx, membershipRefreshInterval)
		err := host.WaitExited(window, ownershipPath)
		cancel()
		if err == nil || ctx.Err() != nil || !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		refresh, cancelRefresh := context.WithTimeout(ctx, killWait)
		_, _ = host.Reconcile(refresh, ownershipPath)
		cancelRefresh()
	}
}

// removeFinishedLaunch deletes the evidence of a launch whose record proves
// its group is finished: an ownership record in a terminal state, or no
// record and no intent at all. Evidence in any other state, and any file the
// host did not write, is left in place for recovery to report.
func removeFinishedLaunch(launchDirectory string) {
	ownershipPath := filepath.Join(launchDirectory, processhost.OwnershipFileName)
	intentPath := filepath.Join(launchDirectory, processhost.LaunchIntentFileName)
	ownership, err := processhost.LoadOwnership(ownershipPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if _, intentErr := os.Lstat(intentPath); !errors.Is(intentErr, os.ErrNotExist) {
			return
		}
	case err != nil:
		return
	case !finishedState(ownership.State):
		return
	default:
		if info, err := os.Lstat(ownershipPath); err != nil || !info.Mode().IsRegular() {
			return
		}
		if err := os.Remove(ownershipPath); err != nil {
			return
		}
	}
	_ = os.Remove(launchDirectory)
}

// finishedState reports whether a persisted ownership state proves the group
// was verified empty: stopped by a verified stop, or observed exited.
func finishedState(state string) bool {
	return state == "stopped" || state == "exited"
}

func (runner Runner) host() *processhost.Host {
	if runner.Processes != nil {
		return runner.Processes
	}
	return NewProcessHost()
}

// createLaunchDirectory proves the runtime root is owned and private, ensures
// the preparation-runs directory beneath it is too, and creates a fresh
// launch directory whose name no earlier launch can have used.
func (runner Runner) createLaunchDirectory() (string, error) {
	root := runner.RuntimeRoot
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsRune(root, 0) {
		return "", ErrInvalidRoot
	}
	if !ownedPrivateDirectory(root) {
		return "", ErrInvalidRoot
	}
	runsRoot := filepath.Join(root, DirectoryName)
	if err := os.Mkdir(runsRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", ErrInvalidRoot
	}
	if !ownedPrivateDirectory(runsRoot) {
		return "", ErrInvalidRoot
	}
	random := runner.Random
	if random == nil {
		random = rand.Reader
	}
	entropy := make([]byte, 16)
	if _, err := io.ReadFull(random, entropy); err != nil {
		return "", err
	}
	launchDirectory := filepath.Join(runsRoot, "launch_"+hex.EncodeToString(entropy))
	if err := os.Mkdir(launchDirectory, 0o700); err != nil {
		return "", ErrInvalidRoot
	}
	if !ownedPrivateDirectory(launchDirectory) {
		_ = os.Remove(launchDirectory)
		return "", ErrInvalidRoot
	}
	return launchDirectory, nil
}

func validateSpec(spec Spec) error {
	if spec.ID == "" || len(spec.ID) > 256 || strings.ContainsAny(spec.ID, "/\x00") ||
		spec.Stdout == nil || spec.Stderr == nil {
		return ErrInvalidSpec
	}
	for _, path := range []string{spec.Executable, spec.Directory} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
			return ErrInvalidSpec
		}
	}
	info, err := os.Lstat(spec.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return ErrInvalidSpec
	}
	directory, err := os.Lstat(spec.Directory)
	if err != nil || !directory.IsDir() {
		return ErrInvalidSpec
	}
	if spec.Timeout <= 0 || spec.Timeout > MaximumTimeout || len(spec.Arguments) > 1024 {
		return ErrInvalidSpec
	}
	for _, argument := range spec.Arguments {
		if strings.ContainsRune(argument, 0) {
			return ErrInvalidSpec
		}
	}
	for _, entry := range spec.Environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" || strings.ContainsRune(entry, 0) {
			return ErrInvalidSpec
		}
	}
	return nil
}

func ownedPrivateDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && info.Mode().Perm()&0o077 == 0
}
