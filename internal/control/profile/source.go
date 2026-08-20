package profile

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
)

const maximumSourceOutputBytes = 16 * 1024 * 1024

type SourceReader struct {
	GitExecutable string
	Now           func() time.Time
}

func (reader SourceReader) Read(ctx context.Context, worktreeRoot string) (environmentcontrol.SourceSnapshot, error) {
	if !filepath.IsAbs(worktreeRoot) || reader.GitExecutable == "" {
		return environmentcontrol.SourceSnapshot{}, ErrProfileInvalid
	}
	revisionOutput, err := reader.git(ctx, worktreeRoot, "rev-parse", "HEAD")
	if err != nil {
		return environmentcontrol.SourceSnapshot{}, err
	}
	revision := strings.TrimSpace(string(revisionOutput))
	if (len(revision) != 40 && len(revision) != 64) || strings.ContainsAny(revision, " \t\r\n\x00") {
		return environmentcontrol.SourceSnapshot{}, ErrProfileInvalid
	}
	status, err := reader.git(ctx, worktreeRoot, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--no-renames")
	if err != nil {
		return environmentcontrol.SourceSnapshot{}, err
	}
	tracked, untracked, err := sourceStatus(status)
	if err != nil {
		return environmentcontrol.SourceSnapshot{}, err
	}
	now := time.Now
	if reader.Now != nil {
		now = reader.Now
	}
	return environmentcontrol.SourceSnapshot{
		Revision: revision, HasTrackedChanges: tracked, HasUntrackedFiles: untracked, ObservedAt: now().UTC(),
	}, nil
}

func (reader SourceReader) git(ctx context.Context, root string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, reader.GitExecutable, append([]string{"-C", root}, arguments...)...)
	var stdout limitedBuffer
	command.Stdout = &stdout
	command.Stderr = nil
	if err := command.Run(); err != nil || stdout.exceeded {
		return nil, errors.New("Git source observation failed")
	}
	return stdout.Bytes(), nil
}

func sourceStatus(contents []byte) (bool, bool, error) {
	if len(contents) == 0 {
		return false, false, nil
	}
	if contents[len(contents)-1] != 0 {
		return false, false, ErrProfileInvalid
	}
	records := bytes.Split(contents[:len(contents)-1], []byte{0})
	if len(records) > 1_000_000 {
		return false, false, ErrProfileInvalid
	}
	tracked, untracked := false, false
	for _, record := range records {
		if len(record) < 4 || record[2] != ' ' {
			return false, false, ErrProfileInvalid
		}
		if record[0] == '?' && record[1] == '?' {
			untracked = true
		} else {
			tracked = true
		}
	}
	return tracked, untracked, nil
}

type limitedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (buffer *limitedBuffer) Write(contents []byte) (int, error) {
	original := len(contents)
	remaining := maximumSourceOutputBytes - buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = true
		return original, nil
	}
	if len(contents) > remaining {
		contents = contents[:remaining]
		buffer.exceeded = true
	}
	_, _ = buffer.Buffer.Write(contents)
	return original, nil
}
