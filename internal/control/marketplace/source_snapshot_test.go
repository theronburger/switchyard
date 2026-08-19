package marketplacecontrol

import (
	"context"
	"reflect"
	"testing"
	"time"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	environmentcontrol "github.com/theronburger/switchyard/internal/control/environment"
)

func TestSourceSnapshotReaderCapturesExactRevisionAndDirtyKinds(t *testing.T) {
	const revision = "0123456789abcdef0123456789abcdef01234567"
	observedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	runner := &recordingRunner{responses: []runnerResponse{
		{output: marketplaceadapter.CommandOutput{Stdout: []byte(revision + "\n")}},
		{output: marketplaceadapter.CommandOutput{Stdout: []byte(" M organizer/a.ts\x00?? scratch.txt\x00")}},
	}}
	reader, err := NewSourceSnapshotReader(runner, "/usr/bin/git")
	if err != nil {
		t.Fatal(err)
	}
	reader.now = func() time.Time { return observedAt }

	source, err := reader.Read(context.Background(), "/tmp/marketplace")
	if err != nil {
		t.Fatal(err)
	}
	want := environmentcontrol.SourceSnapshot{
		Revision: revision, HasTrackedChanges: true, HasUntrackedFiles: true, ObservedAt: observedAt,
	}
	if !reflect.DeepEqual(source, want) {
		t.Fatalf("source: got %#v want %#v", source, want)
	}
	wantInvocations := []marketplaceadapter.Invocation{
		{Executable: "/usr/bin/git", Arguments: []string{"-C", "/tmp/marketplace", "rev-parse", "HEAD"}},
		{Executable: "/usr/bin/git", Arguments: []string{
			"-C", "/tmp/marketplace", "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--no-renames",
		}},
	}
	if !reflect.DeepEqual(runner.invocations, wantInvocations) {
		t.Fatalf("invocations: got %#v want %#v", runner.invocations, wantInvocations)
	}
}

func TestSourceStatusRejectsMalformedRecords(t *testing.T) {
	for name, contents := range map[string][]byte{
		"unterminated":  []byte(" M tracked.ts"),
		"too short":     []byte("??\x00"),
		"bad separator": []byte("M?xtracked.ts\x00"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseSourceStatus(contents); err == nil {
				t.Fatal("malformed source status was accepted")
			}
		})
	}
}
