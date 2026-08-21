package finiterun

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/theronburger/switchyard/internal/runtime/processhost"
)

// maximumRecoveryRuns bounds one boot's scan; anything beyond it is reported
// as truncated rather than silently skipped.
const maximumRecoveryRuns = 10000

// RecoveryReport summarizes one boot's recovery of finite preparation runs.
type RecoveryReport struct {
	// Stopped counts launches whose ownership verified and whose group is
	// now stopped; this includes groups that had already exited on their own.
	Stopped int
	// Unverified counts launches with evidence that could not be positively
	// verified: an intent without ownership, a malformed record, or a group
	// whose live identity no longer matches. They are reported, never
	// signalled, and their evidence is left in place.
	Unverified int
	// Truncated reports that the scan stopped at its bound.
	Truncated bool
}

// RecoverInterruptedRuns finishes every finite preparation process group the
// previous daemon left running. The workspace preparation or environment
// start that launched it is failed as interrupted on boot, so its group must
// not keep executing unowned. Each launch directory under
// runtimeRoot/preparation-runs whose ownership record is still running is
// stopped through the host's verified TERM, grace, KILL sequence, which acts
// only when the persisted leader and members match the live process table by
// PID, process group, start time, and command fingerprint. Nothing is ever
// matched by executable name, and evidence that cannot be verified is counted
// and left alone. A launch that is verified stopped has its evidence removed,
// exactly as a clean finish would have.
func RecoverInterruptedRuns(ctx context.Context, host *processhost.Host, runtimeRoot string) (RecoveryReport, error) {
	report := RecoveryReport{}
	if host == nil {
		return report, errors.New("process host is required")
	}
	if runtimeRoot == "" || !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot {
		return report, ErrInvalidRoot
	}
	runsRoot := filepath.Join(runtimeRoot, DirectoryName)
	if _, err := os.Lstat(runsRoot); errors.Is(err, os.ErrNotExist) {
		return report, nil
	} else if err != nil {
		return report, err
	}
	if !ownedPrivateDirectory(runtimeRoot) || !ownedPrivateDirectory(runsRoot) {
		return report, ErrInvalidRoot
	}
	launches, err := os.ReadDir(runsRoot)
	if err != nil {
		return report, err
	}
	for index, launch := range launches {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if index >= maximumRecoveryRuns {
			report.Truncated = true
			return report, nil
		}
		launchDirectory := filepath.Join(runsRoot, launch.Name())
		if !ownedPrivateDirectory(launchDirectory) {
			continue
		}
		recoverLaunch(ctx, host, launchDirectory, &report)
	}
	return report, nil
}

func recoverLaunch(ctx context.Context, host *processhost.Host, launchDirectory string, report *RecoveryReport) {
	ownershipPath := filepath.Join(launchDirectory, processhost.OwnershipFileName)
	ownership, err := processhost.LoadOwnership(ownershipPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Intent without ownership means the previous daemon died between
		// fork and verification. The candidate leader is evidence only.
		if _, intentErr := os.Lstat(filepath.Join(launchDirectory, processhost.LaunchIntentFileName)); intentErr == nil {
			report.Unverified++
		}
		return
	case err != nil:
		report.Unverified++
		return
	}
	if ownership.EnvironmentID != OwnerScope {
		return
	}
	if ownership.State != "stopped" {
		if _, err := host.Stop(ctx, ownershipPath); err != nil {
			report.Unverified++
			return
		}
		report.Stopped++
	}
	removeFinishedLaunch(launchDirectory)
}
