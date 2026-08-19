package marketplacecontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	marketplaceadapter "github.com/theronburger/switchyard/internal/adapters/marketplace"
	"github.com/theronburger/switchyard/internal/control/inventory"
)

type runnerResponse struct {
	output marketplaceadapter.CommandOutput
	err    error
}

type recordingRunner struct {
	responses   []runnerResponse
	invocations []marketplaceadapter.Invocation
}

func (runner *recordingRunner) Run(
	_ context.Context,
	invocation marketplaceadapter.Invocation,
) (marketplaceadapter.CommandOutput, error) {
	invocation.Arguments = append([]string(nil), invocation.Arguments...)
	runner.invocations = append(runner.invocations, invocation)
	if len(runner.responses) == 0 {
		return marketplaceadapter.CommandOutput{}, errors.New("no fake response")
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	return response.output, response.err
}

func TestMarketplaceRepositoryReaderProjectsAdapterFixtureWithExactArgv(t *testing.T) {
	worktreeFixture := readAdapterWorktreeFixture(t)
	runner := &recordingRunner{responses: []runnerResponse{
		{output: marketplaceadapter.CommandOutput{Stdout: []byte("/Users/example/Marketplace Repo/.git\n")}},
		{output: marketplaceadapter.CommandOutput{Stdout: []byte("/Users/example/Marketplace Repo/.git/info/exclude\n")}},
		{output: marketplaceadapter.CommandOutput{Stdout: []byte("https://credential@github.com/example/marketplace.git\n")}},
		{output: marketplaceadapter.CommandOutput{Stdout: worktreeFixture}},
		{output: marketplaceadapter.CommandOutput{Stdout: []byte("/Users/example/Marketplace Repo/.git\n")}},
		{output: marketplaceadapter.CommandOutput{Stdout: []byte("/Users/example/Marketplace Repo/.git/worktrees/proj-830\n")}},
	}}
	reader, err := NewRepositoryReader(runner, "/usr/bin/git")
	if err != nil {
		t.Fatal(err)
	}
	inventoryService, err := inventory.New(reader)
	if err != nil {
		t.Fatal(err)
	}

	result := inventoryService.DiscoverRepository(context.Background(), "/repo with spaces")
	if result.Repository == nil {
		t.Fatalf("repository discovery failed: %#v", result.Errors)
	}
	if result.Repository.Remote != "example/marketplace" || result.Repository.Adapter != "marketplace" {
		t.Fatalf("repository identity: %#v", result.Repository)
	}
	if len(result.Repository.Worktrees) != 3 {
		t.Fatalf("worktrees: %#v", result.Repository.Worktrees)
	}
	if len(result.Alerts) != 1 || result.Alerts[0].Code != inventory.AlertWorktreePrunable {
		t.Fatalf("alerts: %#v", result.Alerts)
	}
	if len(result.Errors) != 1 || result.Errors[0].Code != inventory.ErrorWorktreeIdentityUnavailable {
		t.Fatalf("errors: %#v", result.Errors)
	}
	if strings.Contains(result.Repository.ID, "credential") || strings.Contains(result.Repository.Remote, "credential") {
		t.Fatalf("remote credentials escaped normalization: %#v", result.Repository)
	}
	if result.ControlPaths.SharedExcludePath != "/Users/example/Marketplace Repo/.git/info/exclude" {
		t.Fatalf("shared exclude path: %#v", result.ControlPaths)
	}

	wantInvocations := []marketplaceadapter.Invocation{
		{
			Executable: "/usr/bin/git",
			Arguments: []string{
				"-C", "/repo with spaces", "rev-parse", "--path-format=absolute", "--git-common-dir",
			},
		},
		{
			Executable: "/usr/bin/git",
			Arguments: []string{
				"-C", "/repo with spaces", "rev-parse", "--path-format=absolute", "--git-path", "info/exclude",
			},
		},
		{
			Executable: "/usr/bin/git",
			Arguments:  []string{"-C", "/repo with spaces", "remote", "get-url", "origin"},
		},
		{
			Executable: "/usr/bin/git",
			Arguments:  []string{"-C", "/repo with spaces", "worktree", "list", "--porcelain", "-z"},
		},
		{
			Executable: "/usr/bin/git",
			Arguments: []string{
				"-C",
				"/Users/example/Developer/marketplace",
				"rev-parse",
				"--path-format=absolute",
				"--absolute-git-dir",
			},
		},
		{
			Executable: "/usr/bin/git",
			Arguments: []string{
				"-C",
				"/Users/example/Developer/marketplace-worktrees/DEMO 42 chapter import",
				"rev-parse",
				"--path-format=absolute",
				"--absolute-git-dir",
			},
		},
	}
	if !reflect.DeepEqual(runner.invocations, wantInvocations) {
		t.Fatalf("invocations:\n got: %#v\nwant: %#v", runner.invocations, wantInvocations)
	}
}

func TestMarketplaceRepositoryReaderDoesNotSurfaceRunnerErrors(t *testing.T) {
	runner := &recordingRunner{responses: []runnerResponse{{
		err: errors.New("https://token@example.invalid AWS_SECRET_ACCESS_KEY=secret"),
	}}}
	reader, err := NewRepositoryReader(runner, "/usr/bin/git")
	if err != nil {
		t.Fatal(err)
	}
	inventoryService, err := inventory.New(reader)
	if err != nil {
		t.Fatal(err)
	}

	result := inventoryService.DiscoverRepository(context.Background(), "/repo")
	if result.Repository != nil || len(result.Errors) != 1 {
		t.Fatalf("result: %#v", result)
	}
	serialized := result.Errors[0].Error() + result.Errors[0].ResourceID
	if strings.Contains(serialized, "token") || strings.Contains(serialized, "secret") ||
		strings.Contains(serialized, "example.invalid") {
		t.Fatalf("runner error escaped sanitization: %q", serialized)
	}
}

func TestNormalizeRemoteStripsCredentialsAndProtocolDifferences(t *testing.T) {
	tests := map[string]string{
		"https": "https://user:token@github.com/example/marketplace.git\n",
		"ssh":   "ssh://git:token@github.com/example/marketplace.git\n",
		"scp":   "token@github.com:example/marketplace.git\n",
	}
	for name, remote := range tests {
		t.Run(name, func(t *testing.T) {
			normalized, valid := normalizeRemote([]byte(remote))
			if !valid || normalized != "example/marketplace" {
				t.Fatalf("normalized remote: got %q, valid %t", normalized, valid)
			}
			if strings.Contains(normalized, "token") {
				t.Fatalf("credentials survived normalization: %q", normalized)
			}
		})
	}
}

func TestNormalizeRemoteRejectsMalformedData(t *testing.T) {
	for name, contents := range map[string][]byte{
		"empty":     {},
		"multiple":  []byte("git@github.com:one/repo.git\ngit@github.com:two/repo.git\n"),
		"local":     []byte("/tmp/repository\n"),
		"traversal": []byte("git@github.com:owner/../repo.git\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, valid := normalizeRemote(contents); valid {
				t.Fatal("expected malformed remote to fail")
			}
		})
	}
}

func readAdapterWorktreeFixture(t *testing.T) []byte {
	t.Helper()
	fixturePath := filepath.Join(
		"..", "..", "adapters", "marketplace", "testdata", "git", "worktrees.porcelain.txt",
	)
	contents, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	return []byte(strings.ReplaceAll(strings.Join(lines, ""), `\0`, "\x00"))
}
