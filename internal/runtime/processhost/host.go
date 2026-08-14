package processhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	defaultGracePeriod  = 5 * time.Second
	defaultKillWait     = 2 * time.Second
	defaultPollInterval = 25 * time.Millisecond
)

type Host struct {
	inspector    ProcessInspector
	signaler     GroupSignaler
	now          func() time.Time
	gracePeriod  time.Duration
	killWait     time.Duration
	pollInterval time.Duration
	locksMutex   sync.Mutex
	runLocks     map[string]*sync.Mutex
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
	return &Host{
		inspector:    config.Inspector,
		signaler:     config.Signaler,
		now:          config.Now,
		gracePeriod:  config.GracePeriod,
		killWait:     config.KillWait,
		pollInterval: config.PollInterval,
		runLocks:     make(map[string]*sync.Mutex),
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
	stdout, err := openAppendOnlyLog(stdoutPath)
	if err != nil {
		return Ownership{}, err
	}
	defer stdout.Close()
	stderr, err := openAppendOnlyLog(stderrPath)
	if err != nil {
		return Ownership{}, err
	}
	defer stderr.Close()

	command := exec.Command(spec.Executable, spec.Arguments...)
	command.Dir = spec.Directory
	if spec.Environment != nil {
		command.Env = append([]string(nil), spec.Environment...)
	}
	command.Stdout = stdout
	command.Stderr = stderr
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
	if err := clearLaunchIntent(intentPath); err != nil {
		go host.recordExit(ownershipPath, ownership.Leader, command)
		stopContext, cancel := context.WithTimeout(context.Background(), host.gracePeriod+host.killWait)
		_, stopErr := host.stopLocked(stopContext, ownershipPath, ownership)
		cancel()
		return Ownership{}, errors.Join(err, stopErr)
	}

	go host.recordExit(ownershipPath, ownership.Leader, command)
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

func (host *Host) stopLocked(ctx context.Context, ownershipPath string, ownership Ownership) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	snapshots, err := host.persistStableMembership(ctx, ownershipPath, &ownership)
	if err != nil {
		return Observation{}, err
	}
	if activeProcessCount(snapshots) == 0 {
		return host.persistStopped(ownershipPath, ownership, snapshots)
	}

	if err := host.signaler.SignalGroup(ownership.ProcessGroupID, syscall.SIGTERM); err != nil {
		return Observation{}, err
	}
	remaining, stopped, err := host.waitForExit(ctx, ownership, host.gracePeriod)
	if err != nil {
		return Observation{}, err
	}
	if stopped {
		return host.persistStopped(ownershipPath, ownership, remaining)
	}

	remaining, err = host.persistStableMembership(ctx, ownershipPath, &ownership)
	if err != nil {
		return Observation{}, err
	}
	if activeProcessCount(remaining) == 0 {
		return host.persistStopped(ownershipPath, ownership, remaining)
	}
	if err := host.signaler.SignalGroup(ownership.ProcessGroupID, syscall.SIGKILL); err != nil {
		return Observation{}, err
	}
	remaining, stopped, err = host.waitForExit(ctx, ownership, host.killWait)
	if err != nil {
		return Observation{}, err
	}
	if !stopped {
		return Observation{}, errors.New("owned process group survived SIGKILL timeout")
	}
	return host.persistStopped(ownershipPath, ownership, remaining)
}

func (host *Host) persistStableMembership(ctx context.Context, ownershipPath string, ownership *Ownership) ([]ProcessSnapshot, error) {
	snapshots, err := stableOwnedGroup(ctx, host.inspector, *ownership)
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

func (host *Host) waitForExit(ctx context.Context, ownership Ownership, timeout time.Duration) ([]ProcessSnapshot, bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(host.pollInterval)
	defer ticker.Stop()

	for {
		snapshots, err := verifyOwnedGroup(ctx, host.inspector, ownership)
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

func (host *Host) recordExit(ownershipPath string, leader ProcessIdentity, command *exec.Cmd) {
	waitErr := command.Wait()
	runLock := host.runLock(ownershipPath)
	runLock.Lock()
	defer runLock.Unlock()

	ownership, err := LoadOwnership(ownershipPath)
	if err != nil || !sameProcessIdentity(leader, ownership.Leader) {
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
