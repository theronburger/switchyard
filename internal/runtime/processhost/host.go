package processhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/theronburger/switchyard/internal/runtime/childgroup"
)

const (
	defaultGracePeriod       = 5 * time.Second
	defaultKillWait          = 2 * time.Second
	defaultPollInterval      = 25 * time.Millisecond
	defaultLeaderSettleDelay = time.Second
)

// startedRun tracks a child this host instance forked. Until reaped is
// closed the kernel cannot reuse the leader's PID, and because reaping and
// signalling both happen under the run lock, a holder of that lock that finds
// reaped still open may treat the PID as positive identity.
type startedRun struct {
	// guarded reports that exit is observed before reaping, so the unreaped
	// guarantee is sound; a run whose exit watch failed is never trusted.
	guarded bool
	// exited closes once the leader's exit was observed; it may still be an
	// unreaped zombie.
	exited chan struct{}
	// reapAllowed, when non-nil, gates reaping until the caller releases it.
	reapAllowed chan struct{}
	allowOnce   sync.Once
	reaped      chan struct{}
	recorded    chan struct{}
}

func (run *startedRun) allowReap() {
	if run.reapAllowed != nil {
		run.allowOnce.Do(func() { close(run.reapAllowed) })
	}
}

type Host struct {
	inspector    ProcessInspector
	signaler     GroupSignaler
	now          func() time.Time
	gracePeriod  time.Duration
	killWait     time.Duration
	pollInterval time.Duration
	settleDelay  time.Duration
	locksMutex   sync.Mutex
	runLocks     map[string]*sync.Mutex
	runs         map[string]*startedRun
}

func New(config Config) *Host {
	if config.Inspector == nil {
		config.Inspector = newSystemInspector()
	}
	if config.Signaler == nil {
		config.Signaler = newSystemSignaler()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.GracePeriod <= 0 {
		config.GracePeriod = defaultGracePeriod
	}
	if config.KillWait <= 0 {
		config.KillWait = defaultKillWait
	}
	if config.PollInterval <= 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.LeaderSettleDelay <= 0 {
		config.LeaderSettleDelay = defaultLeaderSettleDelay
	}
	return &Host{
		inspector:    config.Inspector,
		signaler:     config.Signaler,
		now:          config.Now,
		gracePeriod:  config.GracePeriod,
		killWait:     config.KillWait,
		pollInterval: config.PollInterval,
		settleDelay:  config.LeaderSettleDelay,
		runLocks:     make(map[string]*sync.Mutex),
		runs:         make(map[string]*startedRun),
	}
}

func (host *Host) Start(ctx context.Context, spec LaunchSpec) (Ownership, error) {
	if err := ctx.Err(); err != nil {
		return Ownership{}, err
	}
	if err := validateLaunchSpec(spec); err != nil {
		return Ownership{}, err
	}
	if err := prepareRunDirectory(spec.RunDirectory); err != nil {
		return Ownership{}, err
	}
	ownershipPath := filepath.Join(spec.RunDirectory, OwnershipFileName)
	intentPath := filepath.Join(spec.RunDirectory, LaunchIntentFileName)
	runLock := host.runLock(ownershipPath)
	runLock.Lock()
	defer runLock.Unlock()
	if _, err := os.Lstat(ownershipPath); err == nil {
		return Ownership{}, ErrAlreadyOwned
	} else if !errors.Is(err, os.ErrNotExist) {
		return Ownership{}, err
	}
	if _, err := os.Lstat(intentPath); err == nil {
		return Ownership{}, ErrOrphanUnverified
	} else if !errors.Is(err, os.ErrNotExist) {
		return Ownership{}, err
	}

	stdoutPath := filepath.Join(spec.RunDirectory, StdoutLogFileName)
	stderrPath := filepath.Join(spec.RunDirectory, StderrLogFileName)
	command := exec.Command(spec.Executable, spec.Arguments...)
	command.Dir = spec.Directory
	// The child receives exactly the compiled environment. A nil spec means an
	// empty environment, never the daemon's own, so daemon secrets cannot leak
	// into an owned process group by omission.
	command.Env = append([]string{}, spec.Environment...)
	if spec.Stdout != nil && spec.Stderr != nil {
		command.Stdout = spec.Stdout
		command.Stderr = spec.Stderr
		// A descendant that inherits the pipes and outlives the leader must
		// not keep the exit from being recorded.
		command.WaitDelay = host.killWait
	} else {
		stdout, err := openAppendOnlyLog(stdoutPath)
		if err != nil {
			return Ownership{}, err
		}
		defer func() { _ = stdout.Close() }()
		stderr, err := openAppendOnlyLog(stderrPath)
		if err != nil {
			return Ownership{}, err
		}
		defer func() { _ = stderr.Close() }()
		command.Stdout = stdout
		command.Stderr = stderr
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	now := host.now().UTC()
	intent := LaunchIntent{
		SchemaVersion:     LaunchIntentSchemaVersion,
		EnvironmentID:     spec.EnvironmentID,
		ServiceID:         spec.ServiceID,
		RunID:             spec.RunID,
		Executable:        spec.Executable,
		LaunchFingerprint: fingerprintCommand(spec.Executable, append([]string{spec.Executable}, spec.Arguments...)),
		RunDirectory:      spec.RunDirectory,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := saveLaunchIntent(intentPath, intent); err != nil {
		return Ownership{}, err
	}
	if err := ctx.Err(); err != nil {
		return Ownership{}, errors.Join(err, clearLaunchIntent(intentPath))
	}
	if err := command.Start(); err != nil {
		return Ownership{}, errors.Join(fmt.Errorf("start owned process: %w", err), clearLaunchIntent(intentPath))
	}

	leader, err := host.inspectStartedLeader(ctx, command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return Ownership{}, errors.Join(err, clearLaunchIntent(intentPath))
	}
	if leader.Identity.ProcessGroupID != leader.Identity.PID {
		_ = command.Process.Kill()
		_ = command.Wait()
		return Ownership{}, errors.Join(
			errors.New("started process did not enter its dedicated process group"),
			clearLaunchIntent(intentPath),
		)
	}

	now = host.now().UTC()
	intent.CandidateLeader = &leader.Identity
	intent.UpdatedAt = now
	ownership := Ownership{
		SchemaVersion:     OwnershipSchemaVersion,
		EnvironmentID:     spec.EnvironmentID,
		ServiceID:         spec.ServiceID,
		RunID:             spec.RunID,
		State:             "running",
		ProcessGroupID:    leader.Identity.ProcessGroupID,
		Leader:            leader.Identity,
		Members:           []ProcessIdentity{leader.Identity},
		LaunchFingerprint: intent.LaunchFingerprint,
		StdoutPath:        stdoutPath,
		StderrPath:        stderrPath,
		StartedAt:         leader.Identity.StartedAt,
		UpdatedAt:         now,
	}
	if err := saveLaunchIntent(intentPath, intent); err != nil {
		host.cleanupFailedStart(ownership, command)
		return Ownership{}, err
	}
	if err := saveOwnership(ownershipPath, ownership); err != nil {
		host.cleanupFailedStart(ownership, command)
		return Ownership{}, err
	}
	// The fsynced atomic ownership rename is the commit point. Clearing the
	// intent only after that point means a crash can leave intent-only or both
	// files, but can never erase the evidence before verified ownership exists.
	run := host.registerRun(ownershipPath, ownership.Leader, command, spec.DeferReap)
	if err := clearLaunchIntent(intentPath); err != nil {
		stopContext, cancel := context.WithTimeout(context.Background(), host.gracePeriod+host.killWait)
		_, stopErr := host.stopLocked(stopContext, ownershipPath, ownership)
		cancel()
		return Ownership{}, errors.Join(err, stopErr)
	}
	go host.settleLeader(ownershipPath, run)
	if err := ctx.Err(); err != nil {
		stopContext, cancel := context.WithTimeout(context.Background(), host.gracePeriod+host.killWait)
		_, stopErr := host.stopLocked(stopContext, ownershipPath, ownership)
		cancel()
		return Ownership{}, errors.Join(err, stopErr)
	}
	return ownership, nil
}

func (host *Host) cleanupFailedStart(ownership Ownership, command *exec.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), host.killWait)
	defer cancel()
	if snapshots, err := stableOwnedGroup(ctx, host.inspector, ownership); err == nil && activeProcessCount(snapshots) > 0 {
		_ = host.signaler.SignalGroup(ownership.ProcessGroupID, syscall.SIGKILL)
	}
	// The unreaped process created by this exec.Cmd keeps its PID from being
	// reused, so killing this direct child remains safe even if group
	// verification failed. Descendants are signalled only after verification.
	_ = command.Process.Kill()
	_ = command.Wait()
}

func (host *Host) Reconcile(ctx context.Context, ownershipPath string) (Observation, error) {
	runLock := host.runLock(ownershipPath)
	runLock.Lock()
	defer runLock.Unlock()

	ownership, err := LoadOwnership(ownershipPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			observation, found, observationError := host.observeUnverifiedRun(ctx, ownershipPath)
			if found || observationError != nil {
				return observation, observationError
			}
		}
		return Observation{}, err
	}
	if err := host.requalifyOwnLeader(ctx, ownershipPath, &ownership); err != nil {
		return Observation{}, err
	}
	snapshots, err := verifyOwnedGroup(ctx, host.inspector, ownership)
	if err != nil {
		return Observation{}, err
	}
	now := host.now().UTC()
	ownership.Members = identities(snapshots)
	ownership.UpdatedAt = now
	if activeProcessCount(snapshots) == 0 && ownership.State == "running" {
		ownership.State = "exited"
	}
	if err := saveOwnership(ownershipPath, ownership); err != nil {
		return Observation{}, err
	}
	return observationFromSnapshots(ownershipPath, ownership.State, snapshots, now), nil
}

func (host *Host) Observe(ctx context.Context, ownershipPath string) (Observation, error) {
	return host.Reconcile(ctx, ownershipPath)
}

func (host *Host) Stop(ctx context.Context, ownershipPath string) (Observation, error) {
	runLock := host.runLock(ownershipPath)
	runLock.Lock()
	defer runLock.Unlock()

	ownership, err := LoadOwnership(ownershipPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			observation, found, observationError := host.observeUnverifiedRun(ctx, ownershipPath)
			if found || observationError != nil {
				return observation, errors.Join(ErrOrphanUnverified, observationError)
			}
		}
		return Observation{}, err
	}
	return host.stopLocked(ctx, ownershipPath, ownership)
}

// stopLocked stops the group under the run lock. Membership is verified
// against the persisted identities immediately before every signal. When the
// leader is this host's own unreaped child its PID is positive identity on
// its own: its fingerprint is requalified first, and group members that
// verification cannot tie to a persisted ancestor (for example a grandchild
// reparented to launchd) are still part of a group only this child can have
// created, so the signal proceeds. A leader that is not this host's unreaped
// child keeps the strict rule: any unverified member leaves the group
// untouched and reported.
func (host *Host) stopLocked(ctx context.Context, ownershipPath string, ownership Ownership) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	own := host.unreapedChild(ownershipPath)
	if err := host.requalifyOwnLeader(ctx, ownershipPath, &ownership); err != nil {
		return Observation{}, err
	}
	snapshots, err := host.persistStableMembership(ctx, ownershipPath, &ownership, own)
	if err != nil {
		return Observation{}, err
	}
	if activeProcessCount(snapshots) == 0 {
		return host.persistStopped(ownershipPath, ownership, snapshots)
	}

	// Persisting the stable membership wrote and synced the ownership file;
	// re-verify the group with nothing in between before the signal.
	if snapshots, err = host.groupSnapshots(ctx, ownership, own); err != nil {
		return Observation{}, err
	}
	if activeProcessCount(snapshots) == 0 {
		return host.persistStopped(ownershipPath, ownership, snapshots)
	}
	if err := host.signaler.SignalGroup(ownership.ProcessGroupID, syscall.SIGTERM); err != nil {
		return Observation{}, err
	}
	remaining, stopped, err := host.waitForExit(ctx, ownership, host.gracePeriod, own)
	if err != nil {
		return Observation{}, err
	}
	if stopped {
		return host.persistStopped(ownershipPath, ownership, remaining)
	}

	remaining, err = host.persistStableMembership(ctx, ownershipPath, &ownership, own)
	if err != nil {
		return Observation{}, err
	}
	if activeProcessCount(remaining) == 0 {
		return host.persistStopped(ownershipPath, ownership, remaining)
	}
	if remaining, err = host.groupSnapshots(ctx, ownership, own); err != nil {
		return Observation{}, err
	}
	if activeProcessCount(remaining) == 0 {
		return host.persistStopped(ownershipPath, ownership, remaining)
	}
	if err := host.signaler.SignalGroup(ownership.ProcessGroupID, syscall.SIGKILL); err != nil {
		return Observation{}, err
	}
	remaining, stopped, err = host.waitForExit(ctx, ownership, host.killWait, own)
	if err != nil {
		return Observation{}, err
	}
	if !stopped {
		return Observation{}, errors.New("owned process group survived SIGKILL timeout")
	}
	return host.persistStopped(ownershipPath, ownership, remaining)
}

// groupSnapshots lists the live group. For anyone but the unreaped child's
// own parent, every member must verify against persisted identity.
func (host *Host) groupSnapshots(ctx context.Context, ownership Ownership, own bool) ([]ProcessSnapshot, error) {
	if !own {
		return verifyOwnedGroup(ctx, host.inspector, ownership)
	}
	snapshots, err := host.inspector.ListGroup(ctx, ownership.ProcessGroupID)
	if err != nil {
		return nil, err
	}
	members := make([]ProcessSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.Identity.ProcessGroupID == ownership.ProcessGroupID {
			members = append(members, snapshot)
		}
	}
	sort.Slice(members, func(left, right int) bool {
		return members[left].Identity.PID < members[right].Identity.PID
	})
	return members, nil
}

func (host *Host) stableGroupSnapshots(ctx context.Context, ownership Ownership, own bool) ([]ProcessSnapshot, error) {
	if !own {
		return stableOwnedGroup(ctx, host.inspector, ownership)
	}
	first, err := host.groupSnapshots(ctx, ownership, own)
	if err != nil {
		return nil, err
	}
	second, err := host.groupSnapshots(ctx, ownership, own)
	if err != nil {
		return nil, err
	}
	if !sameSnapshotIdentities(first, second) {
		return nil, ErrUnstableGroup
	}
	return second, nil
}

func (host *Host) persistStableMembership(ctx context.Context, ownershipPath string, ownership *Ownership, own bool) ([]ProcessSnapshot, error) {
	snapshots, err := host.stableGroupSnapshots(ctx, *ownership, own)
	if err != nil {
		return nil, err
	}
	ownership.Members = identities(snapshots)
	ownership.UpdatedAt = host.now().UTC()
	if err := saveOwnership(ownershipPath, *ownership); err != nil {
		return nil, err
	}
	return snapshots, nil
}

func (host *Host) waitForExit(ctx context.Context, ownership Ownership, timeout time.Duration, own bool) ([]ProcessSnapshot, bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(host.pollInterval)
	defer ticker.Stop()

	for {
		snapshots, err := host.groupSnapshots(ctx, ownership, own)
		if err != nil {
			return nil, false, err
		}
		if activeProcessCount(snapshots) == 0 {
			return snapshots, true, nil
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-timer.C:
			return snapshots, false, nil
		case <-ticker.C:
		}
	}
}

func (host *Host) persistStopped(ownershipPath string, ownership Ownership, snapshots []ProcessSnapshot) (Observation, error) {
	now := host.now().UTC()
	ownership.State = "stopped"
	ownership.Members = identities(snapshots)
	ownership.UpdatedAt = now
	if err := saveOwnership(ownershipPath, ownership); err != nil {
		return Observation{}, err
	}
	return observationFromSnapshots(ownershipPath, ownership.State, snapshots, now), nil
}

func (host *Host) inspectStartedLeader(ctx context.Context, pid int) (ProcessSnapshot, error) {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		snapshot, err := host.inspector.Inspect(ctx, pid)
		if err == nil {
			return snapshot, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ProcessSnapshot{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return ProcessSnapshot{}, fmt.Errorf("inspect started process: %w", lastErr)
}

// WaitExited blocks until the leader started by this host at ownershipPath
// has exited. With DeferReap the leader is then still an unreaped zombie, so a
// Stop issued before WaitExit acts on a group whose ID positively denotes
// this run. A run this host did not start reports ErrExitNotObservable.
func (host *Host) WaitExited(ctx context.Context, ownershipPath string) error {
	run := host.startedRun(ownershipPath)
	if run == nil {
		return ErrExitNotObservable
	}
	select {
	case <-run.exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WaitExit releases any deferred reap, blocks until the leader started by this
// host at ownershipPath has been reaped and its exit status recorded, then
// returns that status. A run this host did not start (for example one
// recovered after a restart) reports ErrExitNotObservable; such runs are
// finished only through Stop.
func (host *Host) WaitExit(ctx context.Context, ownershipPath string) (ExitStatus, error) {
	run := host.startedRun(ownershipPath)
	if run == nil {
		return ExitStatus{}, ErrExitNotObservable
	}
	run.allowReap()
	select {
	case <-run.recorded:
	case <-ctx.Done():
		return ExitStatus{}, ctx.Err()
	}
	runLock := host.runLock(ownershipPath)
	runLock.Lock()
	defer runLock.Unlock()
	ownership, err := LoadOwnership(ownershipPath)
	if err != nil {
		return ExitStatus{}, err
	}
	if ownership.Exit == nil {
		return ExitStatus{}, fmt.Errorf("%w: exit status was not recorded", ErrOwnershipInvalid)
	}
	return *ownership.Exit, nil
}

// Forget releases any deferred reap and the host's memory of a run it
// started. It never signals the process or rewrites the record.
func (host *Host) Forget(ownershipPath string) {
	host.locksMutex.Lock()
	defer host.locksMutex.Unlock()
	cleanedPath := filepath.Clean(ownershipPath)
	if run, known := host.runs[cleanedPath]; known {
		run.allowReap()
		delete(host.runs, cleanedPath)
	}
}

func (host *Host) startedRun(ownershipPath string) *startedRun {
	host.locksMutex.Lock()
	defer host.locksMutex.Unlock()
	return host.runs[filepath.Clean(ownershipPath)]
}

// watchExit establishes the kqueue exit watch. Kept as a variable solely so
// tests can force the watch-failure path.
var watchExit = childgroup.WatchExit

// registerRun starts the exit watch for a freshly forked leader. Exit is
// observed through kqueue before Wait ever runs, and Wait runs only under the
// run lock, so every lock holder can rely on reaped to decide whether the PID
// still positively denotes this child. When the watch cannot be established
// the run is recorded as unguarded: Wait then runs outside the run lock for
// the child's whole life, and no lock holder ever treats its PID as positive
// identity.
func (host *Host) registerRun(ownershipPath string, leader ProcessIdentity, command *exec.Cmd, deferReap bool) *startedRun {
	run := &startedRun{exited: make(chan struct{}), reaped: make(chan struct{}), recorded: make(chan struct{})}
	exited, stopWatch, err := watchExit(command.Process.Pid)
	if err != nil {
		exited, stopWatch = nil, func() {}
	}
	run.guarded = err == nil
	if deferReap && run.guarded {
		run.reapAllowed = make(chan struct{})
	}
	host.locksMutex.Lock()
	host.runs[filepath.Clean(ownershipPath)] = run
	host.locksMutex.Unlock()
	go host.recordExit(ownershipPath, leader, command, run, exited, stopWatch)
	return run
}

// unreapedChild reports whether the caller, which must hold the run lock,
// may treat the leader at ownershipPath as this host's own unreaped child.
func (host *Host) unreapedChild(ownershipPath string) bool {
	run := host.startedRun(ownershipPath)
	if run == nil || !run.guarded {
		return false
	}
	select {
	case <-run.reaped:
		return false
	default:
		return true
	}
}

// requalifyOwnLeader refreshes the persisted leader fingerprint of this
// host's own unreaped child. A leader that re-executed itself (a shell shim,
// an env shebang, a version manager) keeps PID, group, and start time; only
// while the caller holds the run lock and the child is unreaped is that PID
// positive identity, so nothing else may requalify a leader. The caller must
// hold the run lock.
func (host *Host) requalifyOwnLeader(ctx context.Context, ownershipPath string, ownership *Ownership) error {
	if !host.unreapedChild(ownershipPath) {
		return nil
	}
	snapshot, err := host.inspector.Inspect(ctx, ownership.Leader.PID)
	if errors.Is(err, ErrProcessNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	live := snapshot.Identity
	if !sameProcessInstance(ownership.Leader, live) || snapshot.Status == "zombie" ||
		live.CommandFingerprint == "" || live.CommandFingerprint == ownership.Leader.CommandFingerprint {
		return nil
	}
	ownership.Leader.CommandFingerprint = live.CommandFingerprint
	for index := range ownership.Members {
		if ownership.Members[index].PID == ownership.Leader.PID {
			ownership.Members[index].CommandFingerprint = live.CommandFingerprint
		}
	}
	ownership.UpdatedAt = host.now().UTC()
	return saveOwnership(ownershipPath, *ownership)
}

func (host *Host) settleLeader(ownershipPath string, run *startedRun) {
	timer := time.NewTimer(host.settleDelay)
	defer timer.Stop()
	select {
	case <-run.reaped:
		return
	case <-timer.C:
	}
	runLock := host.runLock(ownershipPath)
	runLock.Lock()
	defer runLock.Unlock()
	ownership, err := LoadOwnership(ownershipPath)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), host.killWait)
	defer cancel()
	_ = host.requalifyOwnLeader(ctx, ownershipPath, &ownership)
}

func (host *Host) recordExit(ownershipPath string, leader ProcessIdentity, command *exec.Cmd, run *startedRun, exited <-chan struct{}, stopWatch func()) {
	defer close(run.recorded)
	runLock := host.runLock(ownershipPath)
	var waitErr error
	if exited != nil {
		// Reaping happens only after the exit was observed, after any
		// deferral was released, and only under the run lock; closing
		// reaped before anything else lets every later lock holder see it.
		<-exited
		stopWatch()
		close(run.exited)
		if run.reapAllowed != nil {
			<-run.reapAllowed
		}
		runLock.Lock()
		waitErr = command.Wait()
		close(run.reaped)
	} else {
		// Without an exit watch the child was never guarded; Wait must not
		// hold the lock for the child's whole life.
		waitErr = command.Wait()
		close(run.reaped)
		close(run.exited)
		runLock.Lock()
	}
	defer runLock.Unlock()

	ownership, err := LoadOwnership(ownershipPath)
	if err != nil || !sameProcessInstance(leader, ownership.Leader) {
		return
	}
	exit := ExitStatus{ExitedAt: host.now().UTC(), ExitCode: command.ProcessState.ExitCode()}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		if waitStatus, ok := exitError.Sys().(syscall.WaitStatus); ok && waitStatus.Signaled() {
			exit.Signal = int(waitStatus.Signal())
		}
	}
	ownership.Exit = &exit
	ownership.UpdatedAt = exit.ExitedAt
	_ = saveOwnership(ownershipPath, ownership)
}

func (host *Host) runLock(ownershipPath string) *sync.Mutex {
	host.locksMutex.Lock()
	defer host.locksMutex.Unlock()
	cleanedPath := filepath.Clean(ownershipPath)
	if lock, exists := host.runLocks[cleanedPath]; exists {
		return lock
	}
	lock := &sync.Mutex{}
	host.runLocks[cleanedPath] = lock
	return lock
}

func activeProcessCount(snapshots []ProcessSnapshot) int {
	count := 0
	for _, snapshot := range snapshots {
		if snapshot.Status != "zombie" {
			count++
		}
	}
	return count
}

func validateLaunchSpec(spec LaunchSpec) error {
	if spec.EnvironmentID == "" || spec.ServiceID == "" || spec.RunID == "" {
		return errors.New("environment, service, and run ids are required")
	}
	if !filepath.IsAbs(spec.Executable) || !filepath.IsAbs(spec.Directory) || !filepath.IsAbs(spec.RunDirectory) {
		return errors.New("executable, working directory, and run directory must be absolute")
	}
	executableInfo, err := os.Stat(spec.Executable)
	if err != nil {
		return err
	}
	if !executableInfo.Mode().IsRegular() || executableInfo.Mode().Perm()&0o111 == 0 {
		return errors.New("executable must be an executable regular file")
	}
	directoryInfo, err := os.Stat(spec.Directory)
	if err != nil {
		return err
	}
	if !directoryInfo.IsDir() {
		return errors.New("working directory must be a directory")
	}
	for _, argument := range spec.Arguments {
		if len(argument) > 1024*1024 {
			return errors.New("process argument exceeds one MiB")
		}
	}
	return nil
}

func prepareRunDirectory(runDirectory string) error {
	if err := os.MkdirAll(runDirectory, 0o700); err != nil {
		return err
	}
	fileInfo, err := os.Lstat(runDirectory)
	if err != nil {
		return err
	}
	if !fileInfo.IsDir() || fileInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("run directory must be a real directory")
	}
	return os.Chmod(runDirectory, 0o700)
}

func openAppendOnlyLog(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	fileInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !fileInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("log path must be a regular file")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
