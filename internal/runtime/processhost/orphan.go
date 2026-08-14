package processhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

func (host *Host) observeUnverifiedRun(
	ctx context.Context,
	ownershipPath string,
) (Observation, bool, error) {
	runDirectory := filepath.Dir(filepath.Clean(ownershipPath))
	intentPath := filepath.Join(runDirectory, LaunchIntentFileName)
	observation := Observation{
		OwnershipPath: ownershipPath,
		IntentPath:    intentPath,
		State:         StateOrphanUnverified,
		ObservedAt:    host.now().UTC(),
	}

	intent, intentError := LoadLaunchIntent(intentPath)
	if intentError == nil {
		observation.HasLaunchIntent = true
	} else if !errors.Is(intentError, os.ErrNotExist) {
		return observation, true, intentError
	}
	for _, logName := range []string{StdoutLogFileName, StderrLogFileName} {
		if _, err := os.Lstat(filepath.Join(runDirectory, logName)); err == nil {
			observation.HasLogEvidence = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return observation, true, err
		}
	}
	if observation.HasLaunchIntent && intent.CandidateLeader != nil {
		snapshot, err := host.inspector.Inspect(ctx, intent.CandidateLeader.PID)
		switch {
		case err == nil && sameProcessIdentity(*intent.CandidateLeader, snapshot.Identity) &&
			intent.CandidateLeader.ParentPID == snapshot.Identity.ParentPID:
			observation.HasProcessEvidence = true
			if snapshot.Status != "zombie" {
				observation.MemberCount = 1
				observation.MemoryBytes = snapshot.MemoryBytes
				observation.CPUTime = snapshot.CPUTime
			}
		case errors.Is(err, ErrProcessNotFound):
		case err != nil:
			return observation, true, err
		}
	}
	found := observation.HasLaunchIntent || observation.HasLogEvidence
	if !found {
		return Observation{}, false, nil
	}
	return observation, true, nil
}
