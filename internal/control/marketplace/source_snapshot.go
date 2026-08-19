package marketplacecontrol

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"time"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
)

type SourceSnapshotReader struct {
	runner        marketplaceadapter.CommandRunner
	gitExecutable string
	now           func() time.Time
}

func NewSourceSnapshotReader(
	runner marketplaceadapter.CommandRunner,
	gitExecutable string,
) (SourceSnapshotReader, error) {
	if runner == nil || gitExecutable == "" {
		return SourceSnapshotReader{}, errors.New("Marketplace source snapshot reader is unavailable")
	}
	return SourceSnapshotReader{runner: runner, gitExecutable: gitExecutable, now: time.Now}, nil
}

func (reader SourceSnapshotReader) Read(
	ctx context.Context,
	worktreeRoot string,
) (environmentcontrol.SourceSnapshot, error) {
	if !filepath.IsAbs(worktreeRoot) || reader.runner == nil || reader.gitExecutable == "" || reader.now == nil {
		return environmentcontrol.SourceSnapshot{}, errors.New("Marketplace worktree source is invalid")
	}
	revisionOutput, err := reader.git(ctx, worktreeRoot, "rev-parse", "HEAD")
	if err != nil {
		return environmentcontrol.SourceSnapshot{}, errors.New("Marketplace source revision is unavailable")
	}
	revision, valid := parseGitObjectID(revisionOutput)
	if !valid {
		return environmentcontrol.SourceSnapshot{}, errors.New("Marketplace source revision is invalid")
	}
	statusOutput, err := reader.git(ctx, worktreeRoot, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--no-renames")
	if err != nil {
		return environmentcontrol.SourceSnapshot{}, errors.New("Marketplace source status is unavailable")
	}
	tracked, untracked, err := parseSourceStatus(statusOutput)
	if err != nil {
		return environmentcontrol.SourceSnapshot{}, errors.New("Marketplace source status is invalid")
	}
	return environmentcontrol.SourceSnapshot{
		Revision: revision, HasTrackedChanges: tracked, HasUntrackedFiles: untracked,
		ObservedAt: reader.now().UTC(),
	}, nil
}

func (reader SourceSnapshotReader) git(
	ctx context.Context,
	root string,
	arguments ...string,
) ([]byte, error) {
	output, err := reader.runner.Run(ctx, marketplaceadapter.Invocation{
		Executable: reader.gitExecutable,
		Arguments:  append([]string{"-C", root}, arguments...),
	})
	return output.Stdout, err
}

func parseSourceStatus(contents []byte) (bool, bool, error) {
	if len(contents) == 0 {
		return false, false, nil
	}
	if contents[len(contents)-1] != 0 {
		return false, false, errors.New("source status is not NUL terminated")
	}
	records := bytes.Split(contents[:len(contents)-1], []byte{0})
	if len(records) > maximumChangedFiles {
		return false, false, errors.New("source status has too many files")
	}
	tracked := false
	untracked := false
	for _, record := range records {
		if len(record) < 4 || record[2] != ' ' {
			return false, false, errors.New("source status record is malformed")
		}
		if record[0] == '?' && record[1] == '?' {
			untracked = true
		} else {
			tracked = true
		}
	}
	return tracked, untracked, nil
}
