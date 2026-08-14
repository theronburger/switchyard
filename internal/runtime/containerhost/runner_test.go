package containerhost

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOSRunnerHonorsCancellationBeforeStarting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (OSRunner{}).Run(ctx, Command{Executable: "/usr/bin/true"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error: got %v, want cancellation", err)
	}
}

func TestOSRunnerDoesNotExposeArgumentsOrStderr(t *testing.T) {
	secret := "command-secret-must-not-appear"
	_, err := (OSRunner{}).Run(context.Background(), Command{
		Executable: "/usr/bin/false",
		Arguments:  []string{secret},
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unredacted command error: %v", err)
	}
}
