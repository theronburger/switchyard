//go:build darwin

package processhost

import (
	"errors"
	"fmt"
	"syscall"
)

type systemSignaler struct{}

func newSystemSignaler() GroupSignaler {
	return systemSignaler{}
}

func (systemSignaler) SignalGroup(processGroupID int, signal syscall.Signal) error {
	if processGroupID <= 1 {
		return errors.New("refusing to signal an unsafe process group id")
	}
	if err := syscall.Kill(-processGroupID, signal); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("signal owned process group: %w", err)
	}
	return nil
}
