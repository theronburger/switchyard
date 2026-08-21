package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

const (
	terminationGrace = 2 * time.Second
	killWait         = 2 * time.Second
	// stopBudget bounds the verified TERM, grace, re-verify, KILL sequence
	// that finishes a timed-out, cancelled, or straggling group.
	stopBudget = terminationGrace + killWait + 10*time.Second
	// membershipRefreshInterval paces the ownership refresh of a running
	// action so recovery after a crash sees recent leader and member identity.
	membershipRefreshInterval = 15 * time.Second
)

// ExactRunner executes a compiled command in a positively owned process group
// with bounded owner-only stdout/stderr files. It never consults a shell and
// never inherits the daemon environment. RuntimeRoot must be an existing,
// owned, non-symlinked directory; every run directory must sit beneath it and
// every path component between the two must be proven before it is touched.
//
// Ownership is crash-durable: the launch intent and the verified leader
// identity are persisted in the run directory through Processes before and
// immediately after fork, exactly as for environment services, so a daemon
// that dies mid-run leaves evidence RecoverInterruptedRuns can act on with
// positive verification rather than an unowned process group.
type ExactRunner struct {
	RuntimeRoot string
	Now         func() time.Time
	// Processes owns launch, verification, and signalling. When nil a host
	// with the action termination grace is used.
	Processes *processhost.Host
}

// Run executes the command and reports its bounded outcome. A non-zero exit
// status is reported through Outcome, not through the error; errors are
// reserved for invalid commands, start failures, and runner faults. A finite
// action ends with its whole group stopped: descendants that outlive the
// leader are terminated through the same verified ownership.
func (runner ExactRunner) Run(ctx context.Context, command ExactCommand) (Outcome, error) {
	now := runner.Now
	if now == nil {
		now = time.Now
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := validateCommand(command); err != nil {
		return Outcome{}, err
	}
	if err := createOwnedRunDirectory(runner.RuntimeRoot, command.RunDirectory); err != nil {
		return Outcome{}, err
	}
	// A run directory that already carries process evidence is never
	// reused and its logs are never truncated; every operation launches
	// into its own directory.
	for _, evidence := range []string{processhost.OwnershipFileName, processhost.LaunchIntentFileName} {
		if _, err := os.Lstat(filepath.Join(command.RunDirectory, evidence)); err == nil {
			return Outcome{}, ErrInvalidCommand
		} else if !errors.Is(err, os.ErrNotExist) {
			return Outcome{}, err
		}
	}
	stdout, err := openBoundedLog(filepath.Join(command.RunDirectory, processhost.StdoutLogFileName))
	if err != nil {
		return Outcome{}, err
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := openBoundedLog(filepath.Join(command.RunDirectory, processhost.StderrLogFileName))
	if err != nil {
		return Outcome{}, err
	}
	defer func() { _ = stderr.Close() }()

	host := runner.host()
	ownershipPath := filepath.Join(command.RunDirectory, processhost.OwnershipFileName)
	defer host.Forget(ownershipPath)
	runContext, cancel := context.WithTimeout(ctx, command.Timeout)
	defer cancel()
	outcome := Outcome{StartedAt: now().UTC()}
	if _, err := host.Start(runContext, processhost.LaunchSpec{
		EnvironmentID: OwnerScope, ServiceID: command.ActionID, RunID: command.OperationID,
		Executable: command.Executable, Arguments: command.Arguments, Environment: command.Environment,
		Directory: command.Directory, RunDirectory: command.RunDirectory,
		Stdout: stdout, Stderr: stderr, DeferReap: true,
	}); err != nil {
		switch {
		case errors.Is(err, processhost.ErrAlreadyOwned), errors.Is(err, processhost.ErrOrphanUnverified):
			return Outcome{}, ErrInvalidCommand
		case runContext.Err() != nil:
			return Outcome{}, runContext.Err()
		default:
			return Outcome{}, ErrCommandStart
		}
	}

	waitErr := runner.awaitExit(runContext, host, ownershipPath)
	interrupted := waitErr != nil && runContext.Err() != nil
	if waitErr != nil && !interrupted {
		return Outcome{}, waitErr
	}
	// The leader is exited-but-unreaped (or still running on interruption),
	// so its group ID still positively denotes this run while descendants
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
	if interrupted {
		outcome.TimedOut = errors.Is(runContext.Err(), context.DeadlineExceeded)
		if !outcome.TimedOut {
			return Outcome{}, runContext.Err()
		}
	}
	outcome.FinishedAt = now().UTC()
	outcome.StdoutTruncated = stdout.truncated
	outcome.StderrTruncated = stderr.truncated
	outcome.ExitCode = exitCode(exit)
	return outcome, nil
}

// awaitExit waits for the leader while periodically re-reading the owned
// group, so the persisted leader fingerprint and membership a restart would
// recover from stay current for long-running actions.
func (runner ExactRunner) awaitExit(ctx context.Context, host *processhost.Host, ownershipPath string) error {
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

func (runner ExactRunner) host() *processhost.Host {
	if runner.Processes != nil {
		return runner.Processes
	}
	return processhost.New(processhost.Config{GracePeriod: terminationGrace, KillWait: killWait})
}

// NewProcessHost returns the host a daemon should share between its action
// runner and RecoverInterruptedRuns.
func NewProcessHost() *processhost.Host {
	return processhost.New(processhost.Config{GracePeriod: terminationGrace, KillWait: killWait})
}

func exitCode(exit processhost.ExitStatus) int {
	if exit.Signal != 0 {
		return 128 + exit.Signal
	}
	return exit.ExitCode
}

func validateCommand(command ExactCommand) error {
	if !identifierPattern.MatchString(command.ActionID) || !identifierPattern.MatchString(command.OperationID) {
		return ErrInvalidCommand
	}
	for _, path := range []string{command.Executable, command.Directory, command.RunDirectory} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
			return ErrInvalidCommand
		}
	}
	info, err := os.Lstat(command.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return ErrInvalidCommand
	}
	directory, err := os.Lstat(command.Directory)
	if err != nil || !directory.IsDir() {
		return ErrInvalidCommand
	}
	if command.Timeout <= 0 || command.Timeout > MaximumTimeout {
		return ErrInvalidCommand
	}
	if len(command.Arguments) > 1024 {
		return ErrInvalidCommand
	}
	for _, argument := range command.Arguments {
		if strings.ContainsRune(argument, 0) {
			return ErrInvalidCommand
		}
	}
	required := map[string]bool{"HOME": false, "PATH": false, "TMPDIR": false}
	seen := make(map[string]struct{}, len(command.Environment))
	for _, entry := range command.Environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" || strings.ContainsRune(entry, 0) {
			return ErrInvalidCommand
		}
		if _, duplicate := seen[name]; duplicate {
			return ErrInvalidCommand
		}
		seen[name] = struct{}{}
		if _, tracked := required[name]; tracked {
			required[name] = true
		}
	}
	if !required["HOME"] || !required["PATH"] || !required["TMPDIR"] {
		return ErrInvalidCommand
	}
	return nil
}

// createOwnedRunDirectory proves that root is an owned private directory and
// that every existing component between root and destination is a real owned
// private directory before creating the missing remainder. Nothing under a
// symlinked or foreign component is ever created or modified.
func createOwnedRunDirectory(root, destination string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsRune(root, 0) {
		return ErrInvalidCommand
	}
	relative, err := filepath.Rel(root, destination)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrInvalidCommand
	}
	if !ownedPrivateDirectory(root) {
		return ErrInvalidCommand
	}
	components := strings.Split(relative, string(filepath.Separator))
	current := root
	create := len(components)
	for index, component := range components {
		candidate := filepath.Join(current, component)
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			create = index
			break
		}
		if err != nil || !ownedPrivateDirectory(candidate) {
			return ErrInvalidCommand
		}
		current = candidate
	}
	for _, component := range components[create:] {
		current = filepath.Join(current, component)
		if err := os.Mkdir(current, 0o700); err != nil {
			return ErrInvalidCommand
		}
		if !ownedPrivateDirectory(current) {
			return ErrInvalidCommand
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

type boundedLog struct {
	file      *os.File
	remaining int64
	truncated bool
}

// openBoundedLog creates a fresh owner-only log or, when one already exists,
// opens it without following symlinks and proves it is a singly linked owned
// regular file before truncating it. A foreign or multiply linked file is
// never opened for writing with truncation.
func openBoundedLog(path string) (*boundedLog, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, os.ErrExist) {
		file, err = os.OpenFile(path, os.O_WRONLY|syscall.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, ErrInvalidCommand
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, ErrInvalidCommand
	}
	if err := file.Truncate(0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &boundedLog{file: file, remaining: MaximumOutputBytes}, nil
}

func (log *boundedLog) Write(contents []byte) (int, error) {
	original := len(contents)
	if log.remaining <= 0 {
		if original > 0 {
			log.truncated = true
		}
		return original, nil
	}
	if int64(len(contents)) > log.remaining {
		contents = contents[:log.remaining]
		log.truncated = true
	}
	written, err := log.file.Write(contents)
	log.remaining -= int64(written)
	if err != nil {
		return written, err
	}
	return original, nil
}

func (log *boundedLog) Close() error {
	if log == nil || log.file == nil {
		return nil
	}
	return errors.Join(log.file.Sync(), log.file.Close())
}
