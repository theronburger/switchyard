// Package childgroup supervises a started child that leads its own process
// group and terminates that group on interruption without ever signalling a
// process the daemon no longer provably owns.
//
// Go's exec.Cmd.Wait reaps the child through wait4, after which the kernel
// may hand the PID, and with it the process-group ID, to an unrelated
// process. Running Wait concurrently with a timeout therefore races every
// group signal against PID reuse. This package observes the child's exit
// through a kqueue NOTE_EXIT watch instead: until that exit is observed the
// child is an unreaped process of this daemon, its PID cannot be reused, and
// signalling its group is safe. Wait, the only reaper, runs only afterwards.
package childgroup

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Outcome describes how supervision ended.
type Outcome struct {
	// Interrupted reports that ctx ended before the child had exited.
	Interrupted bool
	// Exited reports that the child's exit was observed and it was reaped.
	// When false the child ignored SIGKILL within the grace period (it is
	// typically stuck in the kernel); a background reaper is left behind.
	Exited bool
	// WaitErr is the error returned by Wait when Exited is true.
	WaitErr error
}

// ErrUnsupervisable reports that the child's exit could not be watched.
var ErrUnsupervisable = errors.New("child process exit cannot be observed")

// Supervise waits for cmd, which must already be started with Setpgid, to
// exit. If ctx ends first, the group receives SIGTERM, then SIGKILL after
// grace, each sent only while the child is provably unreaped. cmd.WaitDelay is
// set to grace so a straggler holding the output pipes cannot block Wait
// forever after the child itself has exited.
func Supervise(ctx context.Context, cmd *exec.Cmd, grace time.Duration) Outcome {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return Outcome{WaitErr: ErrUnsupervisable}
	}
	if grace <= 0 {
		grace = 2 * time.Second
	}
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = grace
	}
	pid := cmd.Process.Pid
	exited, stop, err := watchExit(pid)
	if err != nil {
		// The watch could not be established and Wait has not run, so the
		// child is still unreaped: stop it now rather than run unsupervised.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		return Outcome{Interrupted: true, Exited: true, WaitErr: errors.Join(ErrUnsupervisable, cmd.Wait())}
	}
	defer stop()

	outcome := Outcome{}
	select {
	case <-exited:
		return reap(cmd, outcome)
	default:
	}
	select {
	case <-exited:
		return reap(cmd, outcome)
	case <-ctx.Done():
		outcome.Interrupted = true
	}
	// Not yet exited, therefore not yet reaped: the group ID is still ours.
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if waitExit(exited, grace) {
		return reap(cmd, outcome)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	if waitExit(exited, grace) {
		return reap(cmd, outcome)
	}
	// Still alive after SIGKILL. Leave a reaper behind so the zombie is
	// eventually collected; never signal again, because once that reaper
	// runs the PID may belong to someone else.
	go func() { _ = cmd.Wait() }()
	outcome.WaitErr = errors.New("child process group did not exit")
	return outcome
}

func reap(cmd *exec.Cmd, outcome Outcome) Outcome {
	outcome.Exited = true
	outcome.WaitErr = cmd.Wait()
	return outcome
}

func waitExit(exited <-chan struct{}, grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-exited:
		return true
	case <-timer.C:
		return false
	}
}

// watchExit returns a channel that is closed once the process with pid has
// exited (it may still be an unreaped zombie). A process that has already
// exited yields an immediately closed channel.
func watchExit(pid int) (<-chan struct{}, func(), error) {
	exited := make(chan struct{})
	queue, err := unix.Kqueue()
	if err != nil {
		return nil, nil, err
	}
	change := unix.Kevent_t{Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(queue, []unix.Kevent_t{change}, nil, nil); err != nil {
		_ = unix.Close(queue)
		if errors.Is(err, unix.ESRCH) {
			// Already exited; as its parent we have not reaped it, so this
			// is the only way the PID can be unknown to kqueue.
			close(exited)
			return exited, func() {}, nil
		}
		return nil, nil, err
	}
	stopped := make(chan struct{})
	go func() {
		defer func() { _ = unix.Close(queue) }()
		events := make([]unix.Kevent_t, 1)
		poll := unix.NsecToTimespec((250 * time.Millisecond).Nanoseconds())
		for {
			count, err := unix.Kevent(queue, nil, events, &poll)
			if count > 0 {
				close(exited)
				return
			}
			if err != nil && !errors.Is(err, unix.EINTR) {
				// The queue is unusable; treat the child as exited so the
				// caller reaps instead of signalling blind.
				close(exited)
				return
			}
			select {
			case <-stopped:
				return
			default:
			}
		}
	}()
	var once bool
	return exited, func() {
		if !once {
			once = true
			close(stopped)
		}
	}, nil
}
