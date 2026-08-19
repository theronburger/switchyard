package marketplacecontrol

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
)

func TestGitChangeReaderAttributesCommittedUncommittedAndSharedLines(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "organizer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "organizer", "new.ts"), []byte("first\nsecond"), 0o644); err != nil {
		t.Fatal(err)
	}
	const base = "0123456789abcdef0123456789abcdef01234567"
	runner := &recordingRunner{responses: []runnerResponse{
		{output: marketplaceadapter.CommandOutput{Stdout: []byte(base + "\n")}},
		{output: marketplaceadapter.CommandOutput{Stdout: []byte("10\t2\torganizer/a.ts\x005\t1\tpackages/shared/a.ts\x00")}},
		{output: marketplaceadapter.CommandOutput{Stdout: []byte("3\t1\tservices/nonprofit-service/a.ts\x00")}},
		{output: marketplaceadapter.CommandOutput{Stdout: []byte("organizer/new.ts\x00")}},
		{output: marketplaceadapter.CommandOutput{Stdout: []byte("2\n")}},
	}}
	reader, err := NewGitChangeReader(runner, "/usr/bin/git", "origin/main")
	if err != nil {
		t.Fatal(err)
	}

	changes, err := reader.Read(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := WorktreeChanges{
		BaseRevision:      base,
		Committed:         LineChanges{Additions: 15, Deletions: 3, Files: 2},
		Uncommitted:       LineChanges{Additions: 5, Deletions: 1, Files: 2},
		SharedCommitted:   LineChanges{Additions: 5, Deletions: 1, Files: 1},
		SharedUncommitted: LineChanges{},
		Services: []ServiceLineChanges{
			{ServiceID: "nonprofit-service", Uncommitted: LineChanges{Additions: 3, Deletions: 1, Files: 1}},
			{ServiceID: "organizer", Committed: LineChanges{Additions: 10, Deletions: 2, Files: 1}, Uncommitted: LineChanges{Additions: 2, Files: 1}},
		},
		HasTrackedChanges: true, HasUntrackedFiles: true, HasUnpushedCommits: true,
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes:\n got: %#v\nwant: %#v", changes, want)
	}
	wantInvocations := []marketplaceadapter.Invocation{
		{Executable: "/usr/bin/git", Arguments: []string{"-C", root, "merge-base", "origin/main", "HEAD"}},
		{Executable: "/usr/bin/git", Arguments: []string{"-C", root, "diff", "--no-renames", "--numstat", "-z", base, "HEAD", "--"}},
		{Executable: "/usr/bin/git", Arguments: []string{"-C", root, "diff", "--no-renames", "--numstat", "-z", "HEAD", "--"}},
		{Executable: "/usr/bin/git", Arguments: []string{"-C", root, "ls-files", "--others", "--exclude-standard", "-z"}},
		{Executable: "/usr/bin/git", Arguments: []string{"-C", root, "rev-list", "--count", "@{upstream}..HEAD"}},
	}
	if !reflect.DeepEqual(runner.invocations, wantInvocations) {
		t.Fatalf("invocations:\n got: %#v\nwant: %#v", runner.invocations, wantInvocations)
	}
}

func TestGitChangeParsersRejectHostileOutput(t *testing.T) {
	for name, contents := range map[string][]byte{
		"unterminated": []byte("1\t0\torganizer/a.ts"),
		"escape":       []byte("1\t0\t../outside\x00"),
		"negative":     []byte("-1\t0\torganizer/a.ts\x00"),
		"malformed":    []byte("1\torganizer/a.ts\x00"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseNumstat(contents); err == nil {
				t.Fatal("hostile numstat output was accepted")
			}
		})
	}
	if _, err := parseNULPaths([]byte("../../secret\x00")); err == nil {
		t.Fatal("escaping untracked path was accepted")
	}
}
