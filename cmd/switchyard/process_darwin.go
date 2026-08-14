package main

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

func processStartedAt(pid int) (time.Time, error) {
	if pid <= 0 {
		return time.Time{}, errors.New("process pid must be positive")
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, fmt.Errorf("inspect process identity: %w", err)
	}
	if process == nil || int(process.Proc.P_pid) != pid {
		return time.Time{}, errors.New("process identity is unavailable")
	}
	startedAt := time.Unix(0, unix.TimevalToNsec(process.Proc.P_starttime)).UTC()
	if startedAt.IsZero() {
		return time.Time{}, errors.New("process start time is unavailable")
	}
	return startedAt, nil
}

func verifyProcessIdentity(pid int, expectedStartedAt time.Time) error {
	actualStartedAt, err := processStartedAt(pid)
	if err != nil {
		return err
	}
	if !actualStartedAt.Equal(expectedStartedAt) {
		return errors.New("daemon process identity does not match the runtime descriptor")
	}
	return nil
}
