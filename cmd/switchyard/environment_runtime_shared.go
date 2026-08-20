package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"

	contractv2 "github.com/theronburger/switchyard/internal/contract/v2"
	"github.com/theronburger/switchyard/internal/daemon"
	"github.com/theronburger/switchyard/internal/domain"
	"github.com/theronburger/switchyard/internal/runtime/containerhost"
	"github.com/theronburger/switchyard/internal/runtime/portlease"
	"github.com/theronburger/switchyard/internal/state"
)

const (
	dockerExecutableOverride = "SWITCHYARD_DOCKER_EXECUTABLE"
	defaultFirstDynamicPort  = 30000
	defaultLastDynamicPort   = 49999
)

type environmentRuntime struct {
	actions          *daemon.EnvironmentActionService
	workspaceActions *daemon.WorkspaceActionService
	profileActions   *daemon.ProfileActionService
	observerDone     <-chan error
}

func (runtime *environmentRuntime) CloseAndWait(ctx context.Context) error {
	var actionError error
	if runtime.actions != nil {
		actionError = runtime.actions.CloseAndWait(ctx)
	}
	if runtime.workspaceActions != nil {
		actionError = errors.Join(actionError, runtime.workspaceActions.CloseAndWait(ctx))
	}
	if runtime.profileActions != nil {
		actionError = errors.Join(actionError, runtime.profileActions.CloseAndWait(ctx))
	}
	var observerError error
	if runtime.observerDone != nil {
		select {
		case observerError = <-runtime.observerDone:
			if errors.Is(observerError, context.Canceled) {
				observerError = nil
			}
		case <-ctx.Done():
			observerError = ctx.Err()
		}
	}
	return errors.Join(actionError, observerError)
}

func restoreEnvironmentLeases(ctx context.Context, journal *state.EnvironmentJournal, allocator *portlease.Allocator) error {
	after := ""
	for {
		page, err := journal.ListCurrent(ctx, after, state.MaximumCurrentEnvironmentPageSize)
		if err != nil {
			return err
		}
		for _, result := range page.Results {
			if err := allocator.Restore(result.Ports); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		if page.NextEnvironmentID == "" || page.NextEnvironmentID == after {
			return errors.New("environment lease restoration cursor did not advance")
		}
		after = page.NextEnvironmentID
	}
}

func projectedLifecycleStates(state domain.EnvironmentState) (desired string, observed string) {
	switch state {
	case domain.EnvironmentStarting:
		return "running", "starting"
	case domain.EnvironmentRunning:
		return "running", "running"
	case domain.EnvironmentStopping:
		return "stopped", "stopping"
	case domain.EnvironmentStopped:
		return "stopped", "stopped"
	case domain.EnvironmentFailed:
		return "stopped", "failed"
	case domain.EnvironmentOrphaned:
		return "stopped", "orphaned"
	default:
		return "unknown", "unknown"
	}
}

func saturatingResourceAdd(total, value int64) int64 {
	if value <= 0 {
		return total
	}
	if total > int64(^uint64(0)>>1)-value {
		return int64(^uint64(0) >> 1)
	}
	return total + value
}

func stablePortLeaseID(key portlease.Key) string {
	digest := sha256.Sum256([]byte(key.EnvironmentID + "\x00" + key.ServiceID + "\x00" + key.Purpose))
	return "port_" + base64.RawURLEncoding.EncodeToString(digest[:12])
}

func stableInfrastructureLeaseID(identity containerhost.Identity) string {
	digest := sha256.Sum256([]byte(identity.EnvironmentID + "\x00" + identity.ServiceID + "\x00" + identity.RunID + "\x00" + identity.InstanceID))
	return "infra_" + base64.RawURLEncoding.EncodeToString(digest[:12])
}

func environmentDisplayName(worktree contractv2.Worktree) string {
	if worktree.Branch != "" {
		return worktree.Branch
	}
	return filepath.Base(worktree.Path)
}

func configuredDockerExecutable() (string, error) {
	if configured := os.Getenv(dockerExecutableOverride); configured != "" {
		return requireExecutable(configured)
	}
	return requireExecutable("/opt/homebrew/bin/docker")
}

func requireExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("runtime executable path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(resolved) || filepath.Clean(resolved) != resolved {
		return "", errors.New("runtime executable is unavailable")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		return "", errors.New("runtime executable is unavailable")
	}
	return resolved, nil
}
