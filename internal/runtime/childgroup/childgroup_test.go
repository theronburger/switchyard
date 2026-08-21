package childgroup

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func startGroup(t *testing.T, arguments ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command(arguments[0], arguments[1:]...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return command
}

func TestSuperviseReapsNormalExit(t *testing.T) {
	command := startGroup(t, "/bin/sh", "-c", "exit 3")
	outcome := Supervise(context.Background(), command, time.Second)
	if outcome.Interrupted || !outcome.Exited || command.ProcessState == nil || command.ProcessState.ExitCode() != 3 {
		t.Fatalf("outcome=%+v state=%v", outcome, command.ProcessState)
	}
}

func TestSuperviseTerminatesGroupOnInterruption(t *testing.T) {
	command := startGroup(t, "/bin/sh", "-c", "sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	outcome := Supervise(ctx, command, time.Second)
	if !outcome.Interrupted || !outcome.Exited || time.Since(started) > 3*time.Second {
		t.Fatalf("outcome=%+v elapsed=%s", outcome, time.Since(started))
	}
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Fatalf("child was not terminated by SIGTERM: %v", command.ProcessState)
	}
}

func TestSuperviseEscalatesToSIGKILLAfterGrace(t *testing.T) {
	command := startGroup(t, "/bin/sh", "-c", `trap "" TERM; sleep 30`)
	time.Sleep(150 * time.Millisecond) // let the trap install
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	outcome := Supervise(ctx, command, 300*time.Millisecond)
	if !outcome.Interrupted || !outcome.Exited {
		t.Fatalf("outcome=%+v", outcome)
	}
	if status, ok := command.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child was not killed: %v", command.ProcessState)
	}
}

// TestSuperviseNeverSignalsAfterExit starts a child that has already exited
// (an unreaped zombie) under an expired context and proves supervision takes
// the reap path, reporting a normal exit rather than an interruption. A
// signal here would have gone to whatever now owns the group ID.
func TestSuperviseNeverSignalsAfterExit(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		command := startGroup(t, "/usr/bin/true")
		// Wait for the zombie without reaping it.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if isZombie(command.Process.Pid) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		outcome := Supervise(ctx, command, time.Second)
		if outcome.Interrupted || !outcome.Exited || outcome.WaitErr != nil {
			t.Fatalf("iteration %d: exited child was treated as interrupted: %+v", iteration, outcome)
		}
	}
}

// isZombie reports whether pid has exited but is not yet reaped: kqueue
// refuses to watch such a process with ESRCH while kill(pid, 0) still works.
func isZombie(pid int) bool {
	queue, err := unix.Kqueue()
	if err != nil {
		return false
	}
	defer func() { _ = unix.Close(queue) }()
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	_, err = unix.Kevent(queue, []unix.Kevent_t{change}, nil, nil)
	return errors.Is(err, unix.ESRCH)
}
