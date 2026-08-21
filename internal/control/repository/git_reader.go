package repository

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/theronburger/switchyard/internal/control/inventory"
	"github.com/theronburger/switchyard/internal/runtime/helperenv"
)

const maximumGitOutput = 4 * 1024 * 1024

// GitReader observes only generic Git identity and worktree state. The profile
// key labels the private configuration subtree; it does not select product
// code.
type GitReader struct {
	GitExecutable string
	RemoteName    string
	ProfileKey    string
	Run           func(context.Context, string, []string) ([]byte, error)
}

func (reader GitReader) ReadRepository(ctx context.Context, root string) inventory.RepositoryObservation {
	observation := inventory.RepositoryObservation{ProfileKey: reader.ProfileKey}
	if reader.GitExecutable == "" || reader.RemoteName == "" || reader.ProfileKey == "" ||
		!filepath.IsAbs(root) || filepath.Clean(root) != root {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{Code: inventory.ErrorRepositoryGitPathsUnavailable})
		return observation
	}
	run := reader.Run
	if run == nil {
		run = runGit
	}
	commonDirectory, err := readAbsolutePath(ctx, run, reader.GitExecutable, root,
		[]string{"rev-parse", "--path-format=absolute", "--git-common-dir"})
	if err != nil {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{Code: inventory.ErrorRepositoryGitPathsUnavailable})
		return observation
	}
	sharedExclude, err := readAbsolutePath(ctx, run, reader.GitExecutable, root,
		[]string{"rev-parse", "--path-format=absolute", "--git-path", "info/exclude"})
	if err != nil {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{Code: inventory.ErrorRepositoryGitPathsUnavailable})
		return observation
	}
	observation.CommonDirectory = commonDirectory
	observation.SharedExcludePath = sharedExclude

	remoteOutput, err := run(ctx, reader.GitExecutable,
		[]string{"-C", root, "remote", "get-url", reader.RemoteName})
	if err != nil {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{Code: inventory.ErrorRepositoryRemoteUnavailable})
		return observation
	}
	remote, valid := normalizeRemote(remoteOutput)
	if !valid {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{Code: inventory.ErrorRepositoryRemoteUnavailable})
		return observation
	}
	observation.Remote = remote

	worktreeOutput, err := run(ctx, reader.GitExecutable,
		[]string{"-C", root, "worktree", "list", "--porcelain", "-z"})
	if err != nil {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{Code: inventory.ErrorRepositoryWorktreesUnavailable})
		return observation
	}
	worktrees, err := parseWorktrees(worktreeOutput)
	if err != nil {
		observation.Errors = append(observation.Errors, inventory.ErrorObservation{Code: inventory.ErrorRepositoryWorktreesUnavailable})
		return observation
	}
	for index, worktree := range worktrees {
		administrativeIdentity := ""
		if !worktree.prunable {
			administrativeIdentity, _ = readAbsolutePath(ctx, run, reader.GitExecutable, worktree.path,
				[]string{"rev-parse", "--path-format=absolute", "--absolute-git-dir"})
		}
		if administrativeIdentity == "" {
			administrativeIdentity = "registered-path:" + filepath.Clean(worktree.path)
			observation.Errors = append(observation.Errors, inventory.ErrorObservation{
				Code: inventory.ErrorWorktreeIdentityUnavailable, WorktreePath: worktree.path,
			})
		}
		observation.Worktrees = append(observation.Worktrees, inventory.WorktreeObservation{
			Path: worktree.path, AdministrativeIdentity: administrativeIdentity,
			Branch: worktree.branch, HeadRevision: worktree.head, IsPrimary: index == 0,
			Detached: worktree.detached, Bare: worktree.bare, Locked: worktree.locked,
			Prunable: worktree.prunable,
		})
		if worktree.prunable {
			observation.Alerts = append(observation.Alerts, inventory.AlertObservation{
				Code: inventory.AlertWorktreePrunable, WorktreePath: worktree.path,
			})
		}
		if worktree.bare {
			observation.Alerts = append(observation.Alerts, inventory.AlertObservation{
				Code: inventory.AlertWorktreeBare, WorktreePath: worktree.path,
			})
		}
	}
	return observation
}

func runGit(ctx context.Context, executable string, arguments []string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stdout := &boundedBuffer{remaining: maximumGitOutput}
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = helperenv.Sanitized()
	command.Stdout = stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("git command failed")
	}
	if stdout.exceeded {
		return nil, errors.New("git output exceeded its limit")
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

type boundedBuffer struct {
	bytes.Buffer
	remaining int
	exceeded  bool
}

func (buffer *boundedBuffer) Write(contents []byte) (int, error) {
	original := len(contents)
	if len(contents) > buffer.remaining {
		contents = contents[:max(buffer.remaining, 0)]
		buffer.exceeded = true
	}
	buffer.remaining -= len(contents)
	_, _ = buffer.Buffer.Write(contents)
	return original, nil
}

func readAbsolutePath(
	ctx context.Context,
	run func(context.Context, string, []string) ([]byte, error),
	executable, root string,
	arguments []string,
) (string, error) {
	argv := append([]string{"-C", root}, arguments...)
	contents, err := run(ctx, executable, argv)
	if err != nil {
		return "", err
	}
	line, valid := oneLine(contents)
	if !valid || !filepath.IsAbs(line) {
		return "", errors.New("git returned an invalid absolute path")
	}
	return filepath.Clean(line), nil
}

func oneLine(contents []byte) (string, bool) {
	if len(contents) == 0 || bytes.IndexByte(contents, 0) >= 0 || bytes.Count(contents, []byte{'\n'}) > 1 ||
		(bytes.Count(contents, []byte{'\n'}) == 1 && contents[len(contents)-1] != '\n') {
		return "", false
	}
	line := strings.TrimSuffix(strings.TrimSuffix(string(contents), "\n"), "\r")
	return line, line != ""
}

type observedWorktree struct {
	path, head, branch     string
	detached, bare, locked bool
	prunable               bool
}

func parseWorktrees(contents []byte) ([]observedWorktree, error) {
	if len(contents) == 0 {
		return []observedWorktree{}, nil
	}
	if contents[len(contents)-1] != 0 {
		return nil, errors.New("worktree porcelain is not NUL terminated")
	}
	var records [][]string
	var record []string
	for _, field := range bytes.Split(contents, []byte{0}) {
		if len(field) == 0 {
			if len(record) > 0 {
				records = append(records, record)
				record = nil
			}
			continue
		}
		record = append(record, string(field))
	}
	worktrees := make([]observedWorktree, 0, len(records))
	for _, fields := range records {
		worktree, err := parseWorktree(fields)
		if err != nil {
			return nil, err
		}
		worktrees = append(worktrees, worktree)
	}
	return worktrees, nil
}

func parseWorktree(fields []string) (observedWorktree, error) {
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "worktree ") {
		return observedWorktree{}, errors.New("worktree record is invalid")
	}
	worktree := observedWorktree{path: strings.TrimPrefix(fields[0], "worktree ")}
	seen := map[string]bool{"worktree": true}
	for _, field := range fields[1:] {
		key, value, _ := strings.Cut(field, " ")
		if seen[key] {
			return observedWorktree{}, errors.New("worktree field is duplicated")
		}
		seen[key] = true
		switch key {
		case "HEAD":
			if !gitObjectID(value) {
				return observedWorktree{}, errors.New("worktree HEAD is invalid")
			}
			worktree.head = value
		case "branch":
			if !strings.HasPrefix(value, "refs/heads/") || value == "refs/heads/" {
				return observedWorktree{}, errors.New("worktree branch is invalid")
			}
			worktree.branch = strings.TrimPrefix(value, "refs/heads/")
		case "detached":
			worktree.detached = value == ""
		case "bare":
			worktree.bare = value == ""
		case "locked":
			worktree.locked = true
		case "prunable":
			worktree.prunable = true
		default:
			return observedWorktree{}, errors.New("worktree field is unknown")
		}
	}
	checkoutStates := 0
	if worktree.branch != "" {
		checkoutStates++
	}
	if worktree.detached {
		checkoutStates++
	}
	if worktree.bare {
		checkoutStates++
	}
	if worktree.path == "" || worktree.head == "" || checkoutStates != 1 {
		return observedWorktree{}, errors.New("worktree record is incomplete")
	}
	return worktree, nil
}

func gitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func normalizeRemote(contents []byte) (string, bool) {
	remote, valid := oneLine(contents)
	if !valid {
		return "", false
	}
	if strings.Contains(remote, "://") {
		parsed, err := url.Parse(remote)
		if err != nil || parsed.Hostname() == "" {
			return "", false
		}
		path := normalizedRepositoryPath(parsed.Path)
		if path == "" {
			return "", false
		}
		if strings.EqualFold(parsed.Hostname(), "github.com") {
			return strings.ToLower(path), true
		}
		return strings.ToLower(parsed.Hostname()) + "/" + path, true
	}
	separator := strings.IndexByte(remote, ':')
	if separator < 1 {
		return "", false
	}
	host := remote[:separator]
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	path := normalizedRepositoryPath(remote[separator+1:])
	if host == "" || path == "" {
		return "", false
	}
	if strings.EqualFold(host, "github.com") {
		return strings.ToLower(path), true
	}
	return strings.ToLower(host) + "/" + path, true
}

func normalizedRepositoryPath(value string) string {
	value = strings.TrimSuffix(strings.Trim(value, "/"), ".git")
	if value == "" || strings.Contains(value, "..") || strings.ContainsAny(value, "?#\x00\r\n") {
		return ""
	}
	return value
}
