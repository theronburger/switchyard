package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const testGitExecutable = "/Library/Developer/CommandLineTools/usr/bin/git"

func TestManagedManagerCreatesAndArchivesOnlyItsCleanOwnedWorktree(t *testing.T) {
	repository := initializeManagedTestRepository(t)
	managedRoot := filepath.Join(t.TempDir(), "worktrees")
	ownershipRoot := filepath.Join(t.TempDir(), "ownership")
	now := time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)
	manager, err := NewManagedManager(ManagedConfig{
		GitExecutable: testGitExecutable, OwnershipRoot: ownershipRoot,
		Repositories: []ManagedRepository{{
			ID: "repository_01", Root: repository, ManagedRoot: managedRoot, DefaultBase: "main",
		}},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(context.Background(), CreateManagedRequest{
		RepositoryID: "repository_01", Branch: "feature/example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.State != "ready" || created.Branch != "feature/example" ||
		created.WorktreePath == "" || created.AdministrativeGitPath == "" || !created.CreatedAt.Equal(now) {
		t.Fatalf("created worktree: %+v", created)
	}
	assertManagedGitOutput(t, created.WorktreePath, "branch", "--show-current", "feature/example\n")

	foreignFile := filepath.Join(created.WorktreePath, "untracked.txt")
	if err := os.WriteFile(foreignFile, []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = manager.Archive(context.Background(), ArchiveManagedRequest{
		RepositoryID: "repository_01", WorktreePath: created.WorktreePath,
	})
	if !errors.Is(err, ErrManagedDirty) {
		t.Fatalf("dirty archive error: %v", err)
	}
	if _, err := os.Stat(created.WorktreePath); err != nil {
		t.Fatalf("dirty worktree was mutated: %v", err)
	}
	if err := os.Remove(foreignFile); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	archived, err := manager.Archive(context.Background(), ArchiveManagedRequest{
		RepositoryID: "repository_01", WorktreePath: created.WorktreePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if archived.State != "archived" || archived.ArchivedAt == nil || !archived.ArchivedAt.Equal(now) {
		t.Fatalf("archived result: %+v", archived)
	}
	if _, err := os.Lstat(created.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archived worktree still exists: %v", err)
	}
}

func TestManagedManagerRefusesUnpushedAndForeignWorktrees(t *testing.T) {
	repository := initializeManagedTestRepository(t)
	managedRoot := filepath.Join(t.TempDir(), "worktrees")
	manager, err := NewManagedManager(ManagedConfig{
		GitExecutable: testGitExecutable, OwnershipRoot: filepath.Join(t.TempDir(), "ownership"),
		Repositories: []ManagedRepository{{
			ID: "repository_01", Root: repository, ManagedRoot: managedRoot, DefaultBase: "main",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(context.Background(), CreateManagedRequest{
		RepositoryID: "repository_01", Branch: "feature/unpushed",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeManagedTestFile(t, created.WorktreePath, "committed.txt", "new\n")
	runManagedGit(t, created.WorktreePath, "add", "committed.txt")
	runManagedGit(t, created.WorktreePath, "commit", "-m", "local")
	_, err = manager.Archive(context.Background(), ArchiveManagedRequest{
		RepositoryID: "repository_01", WorktreePath: created.WorktreePath,
	})
	if !errors.Is(err, ErrManagedUnpushed) {
		t.Fatalf("unpushed archive error: %v", err)
	}

	// A configured upstream whose ref no longer exists (remote branch deleted
	// and pruned) must not read as "pushed": Git names the upstream but emits
	// no ahead/behind line, so the start-revision comparison must still apply.
	runManagedGit(t, repository, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing-remote"))
	runManagedGit(t, created.WorktreePath, "config", "branch.feature/unpushed.remote", "origin")
	runManagedGit(t, created.WorktreePath, "config", "branch.feature/unpushed.merge", "refs/heads/feature/unpushed")
	_, err = manager.Archive(context.Background(), ArchiveManagedRequest{
		RepositoryID: "repository_01", WorktreePath: created.WorktreePath,
	})
	if !errors.Is(err, ErrManagedUnpushed) {
		t.Fatalf("archive with a vanished upstream ref must refuse unpushed work: %v", err)
	}
	if _, err := os.Stat(created.WorktreePath); err != nil {
		t.Fatalf("unpushed worktree was removed: %v", err)
	}

	foreignPath := filepath.Join(managedRoot, "foreign")
	runManagedGit(t, repository, "worktree", "add", "-b", "feature/foreign", foreignPath, "main")
	_, err = manager.Archive(context.Background(), ArchiveManagedRequest{
		RepositoryID: "repository_01", WorktreePath: foreignPath,
	})
	if !errors.Is(err, ErrManagedForeign) {
		t.Fatalf("foreign archive error: %v", err)
	}
	if _, err := os.Stat(foreignPath); err != nil {
		t.Fatalf("foreign worktree was mutated: %v", err)
	}
}

func TestManagedManagerRejectsHostileBranchBeforeGitMutation(t *testing.T) {
	repository := initializeManagedTestRepository(t)
	managedRoot := filepath.Join(t.TempDir(), "worktrees")
	manager, err := NewManagedManager(ManagedConfig{
		GitExecutable: testGitExecutable, OwnershipRoot: filepath.Join(t.TempDir(), "ownership"),
		Repositories: []ManagedRepository{{
			ID: "repository_01", Root: repository, ManagedRoot: managedRoot, DefaultBase: "main",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Create(context.Background(), CreateManagedRequest{
		RepositoryID: "repository_01", Branch: "feature/../../foreign",
	})
	if !errors.Is(err, ErrManagedRequest) {
		t.Fatalf("hostile branch error: %v", err)
	}
	if _, err := os.Lstat(managedRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hostile request created a managed root: %v", err)
	}
}

func TestManagedManagerAdoptsOnlyCleanPushedLinkedWorktrees(t *testing.T) {
	repository := initializeManagedTestRepository(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runManagedGit(t, filepath.Dir(remote), "init", "--bare", remote)
	runManagedGit(t, repository, "remote", "add", "origin", remote)
	runManagedGit(t, repository, "push", "-u", "origin", "main")
	managedRoot := filepath.Join(t.TempDir(), "worktrees")
	if err := os.MkdirAll(managedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	worktreePath := filepath.Join(managedRoot, "existing")
	runManagedGit(t, repository, "worktree", "add", "-b", "feature/existing", worktreePath, "main")
	runManagedGit(t, worktreePath, "push", "-u", "origin", "HEAD")

	manager, err := NewManagedManager(ManagedConfig{
		GitExecutable: testGitExecutable, OwnershipRoot: filepath.Join(t.TempDir(), "ownership"),
		Repositories: []ManagedRepository{{
			ID: "repository_01", Root: repository, ManagedRoot: managedRoot, DefaultBase: "main",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	adopted, err := manager.Adopt(context.Background(), AdoptManagedRequest{
		RepositoryID: "repository_01", WorktreePath: worktreePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.State != "ready" || adopted.Branch != "feature/existing" || !manager.Owns("repository_01", worktreePath) {
		t.Fatalf("adopted worktree: %+v", adopted)
	}
	repeated, err := manager.Adopt(context.Background(), AdoptManagedRequest{
		RepositoryID: "repository_01", WorktreePath: worktreePath,
	})
	if err != nil || repeated.AdministrativeGitPath != adopted.AdministrativeGitPath {
		t.Fatalf("idempotent adoption: result=%+v err=%v", repeated, err)
	}
	if _, err := manager.Archive(context.Background(), ArchiveManagedRequest{
		RepositoryID: "repository_01", WorktreePath: worktreePath,
	}); err != nil {
		t.Fatalf("archive adopted worktree: %v", err)
	}
}

func TestManagedManagerRefusesDirtyUnpushedAndForeignAdoption(t *testing.T) {
	repository := initializeManagedTestRepository(t)
	managedRoot := filepath.Join(t.TempDir(), "worktrees")
	if err := os.MkdirAll(managedRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManagedManager(ManagedConfig{
		GitExecutable: testGitExecutable, OwnershipRoot: filepath.Join(t.TempDir(), "ownership"),
		Repositories: []ManagedRepository{{
			ID: "repository_01", Root: repository, ManagedRoot: managedRoot, DefaultBase: "main",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unpushedPath := filepath.Join(managedRoot, "unpushed")
	runManagedGit(t, repository, "worktree", "add", "-b", "feature/unpushed-adopt", unpushedPath, "main")
	_, err = manager.Adopt(context.Background(), AdoptManagedRequest{
		RepositoryID: "repository_01", WorktreePath: unpushedPath,
	})
	if !errors.Is(err, ErrManagedUnpushed) {
		t.Fatalf("unpushed adoption error: %v", err)
	}
	writeManagedTestFile(t, unpushedPath, "dirty.txt", "dirty\n")
	_, err = manager.Adopt(context.Background(), AdoptManagedRequest{
		RepositoryID: "repository_01", WorktreePath: unpushedPath,
	})
	if !errors.Is(err, ErrManagedDirty) {
		t.Fatalf("dirty adoption error: %v", err)
	}

	foreignPath := filepath.Join(managedRoot, "foreign")
	if err := os.MkdirAll(foreignPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runManagedGit(t, foreignPath, "init", "-b", "main")
	runManagedGit(t, foreignPath, "config", "user.name", "Switchyard Test")
	runManagedGit(t, foreignPath, "config", "user.email", "switchyard@example.invalid")
	writeManagedTestFile(t, foreignPath, "README.md", "foreign\n")
	runManagedGit(t, foreignPath, "add", "README.md")
	runManagedGit(t, foreignPath, "commit", "-m", "foreign")
	_, err = manager.Adopt(context.Background(), AdoptManagedRequest{
		RepositoryID: "repository_01", WorktreePath: foreignPath,
	})
	if !errors.Is(err, ErrManagedForeign) {
		t.Fatalf("foreign adoption error: %v", err)
	}
}

func initializeManagedTestRepository(t *testing.T) string {
	t.Helper()
	t.Setenv("DEVELOPER_DIR", "/Library/Developer/CommandLineTools")
	root := t.TempDir()
	runManagedGit(t, root, "init", "-b", "main")
	runManagedGit(t, root, "config", "user.name", "Switchyard Test")
	runManagedGit(t, root, "config", "user.email", "switchyard@example.invalid")
	writeManagedTestFile(t, root, "README.md", "test\n")
	runManagedGit(t, root, "add", "README.md")
	runManagedGit(t, root, "commit", "-m", "initial")
	return root
}

func runManagedGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command(testGitExecutable, append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "DEVELOPER_DIR=/Library/Developer/CommandLineTools")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func assertManagedGitOutput(t *testing.T, directory string, argument1 string, argument2 string, want string) {
	t.Helper()
	command := exec.Command(testGitExecutable, "-C", directory, argument1, argument2)
	command.Env = append(os.Environ(), "DEVELOPER_DIR=/Library/Developer/CommandLineTools")
	output, err := command.Output()
	if err != nil || string(output) != want {
		t.Fatalf("git output: got=%q want=%q err=%v", output, want, err)
	}
}

func writeManagedTestFile(t *testing.T, root string, relative string, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, relative), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
