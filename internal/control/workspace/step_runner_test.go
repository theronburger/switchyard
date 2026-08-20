package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExactStepRunnerWritesOnlyPrivateBoundedLogs(t *testing.T) {
	runtimeRoot := t.TempDir()
	working := t.TempDir()
	runDirectory := filepath.Join(runtimeRoot, "repositories", "sample", "run")
	runner := ExactStepRunner{RuntimeRoot: runtimeRoot}
	err := runner.Run(context.Background(), StepSpec{
		ID: "print", Executable: "/bin/echo", Arguments: []string{"hello"},
		Environment: []string{"HOME=/tmp", "PATH=/usr/bin:/bin", "TMPDIR=/tmp"},
		Directory:   working, RunDirectory: runDirectory, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(runDirectory, "stdout.log"))
	if err != nil || string(contents) != "hello\n" {
		t.Fatalf("stdout=%q err=%v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(working, "stdout.log")); !os.IsNotExist(err) {
		t.Fatalf("log leaked into worktree: %v", err)
	}
	marker, err := os.ReadFile(filepath.Join(runDirectory, ownershipMarkerFilename))
	if err != nil || !strings.Contains(string(marker), `"kind":"preparation-step"`) {
		t.Fatalf("ownership marker=%q err=%v", marker, err)
	}
}

func TestExactStepRunnerRejectsSymlinkedPrivatePath(t *testing.T) {
	runtimeRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(runtimeRoot, "repositories")); err != nil {
		t.Fatal(err)
	}
	err := (ExactStepRunner{RuntimeRoot: runtimeRoot}).Run(context.Background(), StepSpec{
		ID: "print", Executable: "/bin/echo", Arguments: []string{"hello"},
		Environment: []string{"HOME=/tmp", "PATH=/usr/bin:/bin", "TMPDIR=/tmp"},
		Directory:   t.TempDir(), RunDirectory: filepath.Join(runtimeRoot, "repositories", "sample"), Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("symlinked private run path was accepted")
	}
}
