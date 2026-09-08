package workspaces

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverKeepsExactWorktreePathsAndPartialRepositoryResults(t *testing.T) {
	repository := gitFixture(t)
	first := addGitWorktree(t, repository, "feature/one", "first/shared")
	second := addGitWorktree(t, repository, "feature/two", "second/shared")
	alias := filepath.Join(filepath.Dir(repository.Path), "alias")
	if err := os.Symlink(repository.Path, alias); err != nil {
		t.Fatal(err)
	}
	repository.Path = alias
	workspaces, err := Discover(context.Background(), []RepositoryConfig{repository, {ID: "missing", Path: filepath.Join(t.TempDir(), "missing")}})
	if len(workspaces) != 3 || err == nil {
		t.Fatalf("partial discovery: %d workspaces, error=%v", len(workspaces), err)
	}
	var failed *GitError
	if !errors.As(err, &failed) || failed.RepositoryID != "missing" || strings.Contains(err.Error(), repository.Path) {
		t.Fatalf("repository error did not remain bounded: %v", err)
	}
	byPath := map[string]Workspace{}
	for _, workspace := range workspaces {
		byPath[workspace.Path] = workspace
		if workspace.GitError != "" || workspace.Unpushed || workspace.Dirty {
			t.Fatalf("fresh worktree inspection: %+v", workspace)
		}
	}
	if byPath[first].Branch != "feature/one" || byPath[second].Branch != "feature/two" {
		t.Fatalf("same directory basenames mixed: %+v", workspaces)
	}
	if !byPath[mustPhysical(t, alias)].Primary || byPath[first].Primary || byPath[second].Primary {
		t.Fatalf("primary classification: %+v", workspaces)
	}
	plan, err := GitPrunePlan(context.Background(), repository, filepath.Join(first, "nested"))
	if err != nil || len(plan.Blockers) == 0 {
		t.Fatalf("nested directory was treated as the registered worktree: %+v %v", plan, err)
	}
}

func TestDiscoverKeepsSameBranchInDifferentRepositoriesSeparate(t *testing.T) {
	first, second := gitFixture(t), gitFixture(t)
	second.ID = "other"
	firstPath := addGitWorktree(t, first, "feature/shared", "shared")
	secondPath := addGitWorktree(t, second, "feature/shared", "shared")
	workspaces, err := Discover(context.Background(), []RepositoryConfig{first, second})
	if err != nil || len(workspaces) != 4 {
		t.Fatalf("two repository discovery: %d %v", len(workspaces), err)
	}
	byPath := map[string]Workspace{}
	for _, workspace := range workspaces {
		byPath[workspace.Path] = workspace
	}
	if byPath[firstPath].RepositoryID != first.ID || byPath[secondPath].RepositoryID != second.ID ||
		byPath[firstPath].Branch != "feature/shared" || byPath[secondPath].Branch != "feature/shared" {
		t.Fatalf("same branch workspaces were mixed: %+v", workspaces)
	}
	if err := GitPrune(context.Background(), first, firstPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(secondPath); err != nil {
		t.Fatalf("pruning first removed the same branch in another repository: %v", err)
	}
}

func TestGitPruneProtectsDirtyUnpushedLockedPrimaryAndForeignWorktrees(t *testing.T) {
	repository := gitFixture(t)
	dirty := addGitWorktree(t, repository, "dirty", "dirty")
	unpushed := addGitWorktree(t, repository, "unpushed", "unpushed")
	locked := addGitWorktree(t, repository, "locked", "locked")
	clean := addGitWorktree(t, repository, "clean", "clean")
	foreignRepository := gitFixture(t)
	foreign := addGitWorktree(t, foreignRepository, "foreign", "foreign")
	writeGitFile(t, dirty, "untracked.txt", "keep me")
	writeGitFile(t, unpushed, "change.txt", "unique commit")
	gitTest(t, unpushed, "add", "change.txt")
	gitTest(t, unpushed, "commit", "-m", "unpushed")
	gitTest(t, repository.Path, "worktree", "lock", locked)
	for _, test := range []struct{ path, blocker string }{
		{repository.Path, "primary"}, {dirty, "local changes"}, {unpushed, "not present on a remote"},
		{locked, "locked"}, {foreign, "not registered"},
	} {
		plan, err := GitPrunePlan(context.Background(), repository, test.path)
		if err != nil || !strings.Contains(strings.Join(plan.Blockers, " "), test.blocker) {
			t.Fatalf("missing blocker %q: %+v, %v", test.blocker, plan, err)
		}
		if err := GitPrune(context.Background(), repository, test.path); err == nil {
			t.Fatalf("removed protected directory %q", test.blocker)
		}
		if _, err := os.Stat(test.path); err != nil {
			t.Fatalf("protected directory disappeared: %v", err)
		}
	}
	plan, err := GitPrunePlan(context.Background(), repository, clean)
	if err != nil || len(plan.Blockers) != 0 || plan.Path != clean {
		t.Fatalf("clean plan: %+v, %v", plan, err)
	}
	if err := GitPrune(context.Background(), repository, clean); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(clean); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean directory remains: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign worktree removed: %v", err)
	}
	if got := gitTest(t, foreignRepository.Path, "worktree", "list", "--porcelain"); !strings.Contains(got, foreign) {
		t.Fatal("foreign registration changed")
	}
}

func TestGitPruneRechecksChangesAndMissingRegistrationRemovalIsExact(t *testing.T) {
	repository := gitFixture(t)
	changed := addGitWorktree(t, repository, "changed", "changed")
	missing := addGitWorktree(t, repository, "missing", "missing")
	otherMissing := addGitWorktree(t, repository, "other-missing", "other-missing")
	plan, err := GitPrunePlan(context.Background(), repository, changed)
	if err != nil || len(plan.Blockers) != 0 {
		t.Fatalf("initial plan: %+v %v", plan, err)
	}
	writeGitFile(t, changed, "later.txt", "must survive")
	if err := GitPrune(context.Background(), repository, changed); err == nil {
		t.Fatal("removed worktree changed after preview")
	}
	for _, path := range []string{missing, otherMissing} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	plan, err = GitPrunePlan(context.Background(), repository, missing)
	if err != nil || len(plan.Blockers) != 0 {
		t.Fatalf("missing directory plan: %+v %v", plan, err)
	}
	if err := GitPrune(context.Background(), repository, missing); err != nil {
		t.Fatal(err)
	}
	workspaces, err := Discover(context.Background(), []RepositoryConfig{repository})
	if err != nil {
		t.Fatal(err)
	}
	otherFound := false
	for _, workspace := range workspaces {
		if workspace.Path == missing {
			t.Fatal("removed registration is still listed")
		}
		if workspace.Path == otherMissing && workspace.Missing {
			otherFound = true
		}
	}
	if !otherFound {
		t.Fatal("removal pruned another missing registration")
	}
}

func TestGitDiscoveryAndPruneRejectReplacedWorktree(t *testing.T) {
	repository := gitFixture(t)
	path := addGitWorktree(t, repository, "replaced", "replaced")
	if err := os.Remove(filepath.Join(path, ".git")); err != nil {
		t.Fatal(err)
	}
	gitTest(t, path, "init", "-b", "foreign")
	gitTest(t, path, "add", ".")
	gitTest(t, path, "commit", "-m", "foreign repository")
	plan, err := GitPrunePlan(context.Background(), repository, path)
	if err != nil || !strings.Contains(strings.Join(plan.Blockers, " "), "no longer matches") {
		t.Fatalf("replaced worktree was not protected: %+v %v", plan, err)
	}
	if err := GitPrune(context.Background(), repository, path); err == nil {
		t.Fatal("removed replacement repository")
	}
}

func TestGitCreateOrdinaryWorktreesAndPreservesExistingBranches(t *testing.T) {
	repository := gitFixture(t)
	created, err := GitCreate(context.Background(), repository, CreateRequest{RepositoryID: repository.ID, Branch: "feature/new", Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(filepath.Dir(repository.Path), filepath.Base(repository.Path)+"-worktrees", "feature", "new")
	if created != expected || strings.TrimSpace(gitTest(t, created, "branch", "--show-current")) != "feature/new" {
		t.Fatalf("created worktree: %q", created)
	}
	if _, err := GitCreate(context.Background(), repository, CreateRequest{RepositoryID: repository.ID, Branch: "feature/new"}); err == nil {
		t.Fatal("reused existing worktree directory")
	}
	gitTest(t, repository.Path, "branch", "existing", "main")
	repository.WorktreesPath = filepath.Join(filepath.Dir(repository.Path), "custom worktrees")
	existing, err := GitCreate(context.Background(), repository, CreateRequest{RepositoryID: repository.ID, Branch: "existing"})
	if err != nil || existing != filepath.Join(repository.WorktreesPath, "existing") {
		t.Fatalf("existing branch: %q %v", existing, err)
	}
	if diff := gitTest(t, repository.Path, "status", "--porcelain"); diff != "" {
		t.Fatalf("create changed repository files: %q", diff)
	}
	for _, branch := range []string{"../escape", "-bad", "@{-1}", "invalid name"} {
		if _, err := GitCreate(context.Background(), repository, CreateRequest{RepositoryID: repository.ID, Branch: branch}); err == nil {
			t.Fatalf("accepted invalid branch %q", branch)
		}
	}
}

func TestGitDiscoverPhysicalPathsWithNewlinesAndCancelledRead(t *testing.T) {
	repository := gitFixture(t)
	path := addGitWorktree(t, repository, "newline", "line\nbreak")
	workspaces, err := Discover(context.Background(), []RepositoryConfig{repository})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, workspace := range workspaces {
		if workspace.Path == path && workspace.GitError == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("NUL-delimited path was lost: %+v", workspaces)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, []RepositoryConfig{repository}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled discovery: %v", err)
	}
}

func TestResolvePathUsesNestedRepositoryIdentityAndExactMissingPath(t *testing.T) {
	repository := gitFixture(t)
	subdirectory := filepath.Join(repository.Path, "packages")
	if err := os.Mkdir(subdirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolvePath(context.Background(), subdirectory)
	if err != nil || resolved != repository.Path {
		t.Fatalf("child directory did not resolve to root: %q %v", resolved, err)
	}
	nested := filepath.Join(subdirectory, "nested")
	gitTest(t, repository.Path, "init", "-b", "main", nested)
	resolved, err = ResolvePath(context.Background(), nested)
	if err != nil || resolved != nested {
		t.Fatalf("nested repository was attributed to outer worktree: %q %v", resolved, err)
	}
	missing := filepath.Join(filepath.Dir(repository.Path), "missing", "child")
	resolved, err = ResolvePath(context.Background(), missing)
	if err != nil || resolved != missing {
		t.Fatalf("missing path was not retained exactly: %q %v", resolved, err)
	}
}

func TestGitCreateRejectsSymlinkBranchDirectoryBeforeWriting(t *testing.T) {
	repository := gitFixture(t)
	repository.WorktreesPath = filepath.Join(filepath.Dir(repository.Path), "worktrees")
	foreign := mustPhysical(t, t.TempDir())
	if err := os.Mkdir(repository.WorktreesPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(repository.WorktreesPath, "feature")); err != nil {
		t.Fatal(err)
	}
	_, err := GitCreate(context.Background(), repository, CreateRequest{RepositoryID: repository.ID, Branch: "feature/nested/new"})
	if err == nil {
		t.Fatal("branch directory escaped worktrees root")
	}
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected creation wrote into foreign directory: %v %v", entries, err)
	}
}

func gitFixture(t *testing.T) RepositoryConfig {
	t.Helper()
	directory := mustPhysical(t, t.TempDir())
	root, remote := filepath.Join(directory, "repository"), filepath.Join(directory, "remote.git")
	gitTest(t, directory, "init", "--bare", remote)
	gitTest(t, directory, "init", "-b", "main", root)
	writeGitFile(t, root, "README.md", "fixture\n")
	gitTest(t, root, "add", "README.md")
	gitTest(t, root, "commit", "-m", "initial")
	gitTest(t, root, "remote", "add", "origin", remote)
	gitTest(t, root, "push", "-u", "origin", "main")
	return RepositoryConfig{ID: "sample", Name: "Sample", Path: root}
}

func addGitWorktree(t *testing.T, repository RepositoryConfig, branch, relative string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(repository.Path), relative)
	gitTest(t, repository.Path, "worktree", "add", "-b", branch, path, "main")
	return mustPhysical(t, path)
}

func gitTest(t *testing.T, path string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, arguments...)...)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git %s: %s (%v)", arguments[0], output, err)
	}
	return string(output)
}

func mustPhysical(t *testing.T, path string) string {
	t.Helper()
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return physical
}

func writeGitFile(t *testing.T, directory, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
