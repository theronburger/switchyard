package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

// ActionsDirectoryName is the directory under the runtime root that holds
// one run directory per finite action operation, grouped by profile key.
const ActionsDirectoryName = "actions"

// maximumRecoveryRuns bounds one boot's scan; anything beyond it is reported
// as truncated rather than silently skipped.
const maximumRecoveryRuns = 10000

// RecoveryReport summarizes one boot's recovery of finite action runs.
type RecoveryReport struct {
	// Stopped counts runs whose ownership verified and whose group is now
	// stopped; this includes groups that had already exited on their own.
	Stopped int
	// Unverified counts runs with evidence that could not be positively
	// verified: an intent without ownership, a malformed record, or a group
	// whose live identity no longer matches. They are reported, never
	// signalled.
	Unverified int
	// Truncated reports that the scan stopped at its bound.
	Truncated bool
}

// RecoverInterruptedRuns finishes every finite action process group the
// previous daemon left running. A finite action whose operation the restart
// has failed must not keep executing unowned, so each run directory under
// runtimeRoot/actions whose ownership record is still running is stopped
// through the host's verified TERM, grace, KILL sequence. Stop acts only when
// the persisted leader and members match the live process table by PID,
// process group, start time, and command fingerprint; nothing is ever matched
// by executable name, and evidence that cannot be verified is counted and
// left alone.
func RecoverInterruptedRuns(ctx context.Context, host *processhost.Host, runtimeRoot string) (RecoveryReport, error) {
	report := RecoveryReport{}
	if host == nil {
		return report, errors.New("process host is required")
	}
	if runtimeRoot == "" || !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot {
		return report, ErrInvalidCommand
	}
	actionsRoot := filepath.Join(runtimeRoot, ActionsDirectoryName)
	if _, err := os.Lstat(actionsRoot); errors.Is(err, os.ErrNotExist) {
		return report, nil
	} else if err != nil {
		return report, err
	}
	if !ownedPrivateDirectory(runtimeRoot) || !ownedPrivateDirectory(actionsRoot) {
		return report, ErrInvalidCommand
	}
	profiles, err := os.ReadDir(actionsRoot)
	if err != nil {
		return report, err
	}
	scanned := 0
	for _, profile := range profiles {
		profileRoot := filepath.Join(actionsRoot, profile.Name())
		if !ownedPrivateDirectory(profileRoot) {
			continue
		}
		runs, err := os.ReadDir(profileRoot)
		if err != nil {
			return report, err
		}
		for _, run := range runs {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			if scanned >= maximumRecoveryRuns {
				report.Truncated = true
				return report, nil
			}
			scanned++
			runDirectory := filepath.Join(profileRoot, run.Name())
			if !ownedPrivateDirectory(runDirectory) {
				continue
			}
			recoverRun(ctx, host, runDirectory, &report)
		}
	}
	return report, nil
}

func recoverRun(ctx context.Context, host *processhost.Host, runDirectory string, report *RecoveryReport) {
	ownershipPath := filepath.Join(runDirectory, processhost.OwnershipFileName)
	ownership, err := processhost.LoadOwnership(ownershipPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Intent without ownership means the previous daemon died between
		// fork and verification. The candidate leader is evidence only.
		if _, intentErr := os.Lstat(filepath.Join(runDirectory, processhost.LaunchIntentFileName)); intentErr == nil {
			report.Unverified++
		}
		return
	case err != nil:
		report.Unverified++
		return
	}
	if ownership.EnvironmentID != OwnerScope || ownership.State != "running" {
		return
	}
	if _, err := host.Stop(ctx, ownershipPath); err != nil {
		report.Unverified++
		return
	}
	report.Stopped++
}
