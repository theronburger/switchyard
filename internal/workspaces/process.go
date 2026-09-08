package workspaces

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

type processSpec struct {
	Script      string   `json:"script"`
	Directory   string   `json:"directory"`
	Environment []string `json:"environment"`
}

type managedProcess struct {
	command *exec.Cmd
	input   io.WriteCloser
	done    chan struct{}
	err     error
	once    sync.Once
}

func launchProcess(ctx context.Context, spec processSpec, logs io.Writer) (*managedProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, errors.New("could not locate the Switchyard helper")
	}
	command := exec.Command(executable, "--supervise")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdout, command.Stderr = logs, logs
	input, err := command.StdinPipe()
	if err != nil {
		return nil, errors.New("could not create the command supervisor")
	}
	if err := command.Start(); err != nil {
		_ = input.Close()
		return nil, errors.New("could not start the command supervisor")
	}
	process := &managedProcess{command: command, input: input, done: make(chan struct{})}
	go func() { process.err = command.Wait(); close(process.done) }()
	if err := json.NewEncoder(input).Encode(spec); err != nil {
		process.stop()
		return nil, errors.New("could not send the command to its supervisor")
	}
	return process, nil
}

func (process *managedProcess) stop() {
	process.once.Do(func() { _ = process.input.Close() })
	<-process.done
}

func (process *managedProcess) exited() bool {
	select {
	case <-process.done:
		return true
	default:
		return false
	}
}

// Supervise keeps its own process group alive until every command descendant
// has been stopped. Losing the daemon's stdin pipe ends that group as well.
func Supervise() int {
	if syscall.Getpgrp() != os.Getpid() {
		return 125
	}
	reader := bufio.NewReader(io.LimitReader(os.Stdin, 4*1024*1024))
	payload, err := reader.ReadBytes('\n')
	if err != nil {
		return 125
	}
	var spec processSpec
	if json.Unmarshal(payload, &spec) != nil || spec.Script == "" || spec.Directory == "" {
		return 125
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	command := exec.Command("/bin/zsh", "-f", "-c", spec.Script)
	command.Dir, command.Env = spec.Directory, spec.Environment
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return 126
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	disconnected := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, reader); close(disconnected) }()
	var commandError error
	finished := false
	select {
	case commandError = <-exited:
		finished = true
	case <-disconnected:
	case <-signals:
	}
	_ = syscall.Kill(-os.Getpid(), syscall.SIGTERM)
	if !finished {
		select {
		case commandError = <-exited:
			finished = true
		case <-time.After(1500 * time.Millisecond):
		}
	}
	// The guardian remains the group leader until KILL, so its PID cannot be
	// reused for a foreign process group while descendants are being stopped.
	if !finished {
		_ = syscall.Kill(-os.Getpid(), syscall.SIGKILL)
		return 137
	}
	if groupHasDescendants() {
		time.Sleep(100 * time.Millisecond)
		if groupHasDescendants() {
			_ = syscall.Kill(-os.Getpid(), syscall.SIGKILL)
			return 137
		}
	}
	if commandError != nil {
		return 1
	}
	return 0
}

func groupHasDescendants() bool {
	// A signal to the group includes this guardian; checking only its child
	// would miss a background child left behind by a completed shell.
	command := exec.Command("/bin/ps", "-axo", "pid=,pgid=")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output, err := command.Output()
	if err != nil {
		return true
	}
	return groupContainsOtherPID(output, os.Getpid())
}
