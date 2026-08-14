package marketplace

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"strings"
)

type Worktree struct {
	Path           string
	HeadRevision   string
	Branch         string
	IsPrimary      bool
	Detached       bool
	Bare           bool
	Locked         bool
	LockReason     string
	Prunable       bool
	PrunableReason string
}

type GitDiscovery struct {
	runner        CommandRunner
	gitExecutable string
}

func NewGitDiscovery(runner CommandRunner, gitExecutable string) GitDiscovery {
	return GitDiscovery{runner: runner, gitExecutable: gitExecutable}
}

func (discovery GitDiscovery) ListWorktrees(ctx context.Context, repositoryRoot string) ([]Worktree, error) {
	if discovery.runner == nil {
		return nil, fmt.Errorf("list git worktrees: command runner is required")
	}
	if discovery.gitExecutable == "" {
		return nil, fmt.Errorf("list git worktrees: git executable is required")
	}
	if repositoryRoot == "" {
		return nil, fmt.Errorf("list git worktrees: repository root is required")
	}

	output, err := discovery.runner.Run(ctx, Invocation{
		Executable: discovery.gitExecutable,
		Arguments: []string{
			"-C",
			repositoryRoot,
			"worktree",
			"list",
			"--porcelain",
			"-z",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list git worktrees: %w", err)
	}

	worktrees, err := ParseWorktreePorcelain(output.Stdout)
	if err != nil {
		return nil, fmt.Errorf("parse git worktrees: %w", err)
	}
	return worktrees, nil
}

func ParseWorktreePorcelain(contents []byte) ([]Worktree, error) {
	if len(contents) == 0 {
		return []Worktree{}, nil
	}
	if contents[len(contents)-1] != 0 {
		return nil, fmt.Errorf("porcelain output is not NUL terminated")
	}

	var records [][]string
	var record []string
	for _, rawField := range bytes.Split(contents, []byte{0}) {
		if len(rawField) == 0 {
			if len(record) > 0 {
				records = append(records, record)
				record = nil
			}
			continue
		}
		record = append(record, string(rawField))
	}

	worktrees := make([]Worktree, 0, len(records))
	for recordIndex, fields := range records {
		worktree, err := parseWorktreeRecord(fields)
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", recordIndex+1, err)
		}
		worktree.IsPrimary = recordIndex == 0
		worktrees = append(worktrees, worktree)
	}
	return worktrees, nil
}

func parseWorktreeRecord(fields []string) (Worktree, error) {
	if len(fields) == 0 {
		return Worktree{}, fmt.Errorf("record is empty")
	}
	if !strings.HasPrefix(fields[0], "worktree ") {
		return Worktree{}, fmt.Errorf("first field must be worktree")
	}

	worktree := Worktree{Path: strings.TrimPrefix(fields[0], "worktree ")}
	if worktree.Path == "" {
		return Worktree{}, fmt.Errorf("worktree path is empty")
	}

	seen := map[string]bool{"worktree": true}
	for _, field := range fields[1:] {
		key, value, _ := strings.Cut(field, " ")
		if seen[key] {
			return Worktree{}, fmt.Errorf("duplicate %s field", key)
		}
		seen[key] = true

		switch key {
		case "HEAD":
			if !isGitObjectID(value) {
				return Worktree{}, fmt.Errorf("HEAD is not a 40- or 64-character hexadecimal object ID")
			}
			worktree.HeadRevision = value
		case "branch":
			const localBranchPrefix = "refs/heads/"
			if !strings.HasPrefix(value, localBranchPrefix) || value == localBranchPrefix {
				return Worktree{}, fmt.Errorf("branch is not a local branch reference")
			}
			worktree.Branch = strings.TrimPrefix(value, localBranchPrefix)
		case "detached":
			if value != "" {
				return Worktree{}, fmt.Errorf("detached field has an unexpected value")
			}
			worktree.Detached = true
		case "bare":
			if value != "" {
				return Worktree{}, fmt.Errorf("bare field has an unexpected value")
			}
			worktree.Bare = true
		case "locked":
			worktree.Locked = true
			worktree.LockReason = value
		case "prunable":
			worktree.Prunable = true
			worktree.PrunableReason = value
		default:
			return Worktree{}, fmt.Errorf("unknown field %q", key)
		}
	}

	if worktree.HeadRevision == "" {
		return Worktree{}, fmt.Errorf("HEAD field is required")
	}
	checkoutStates := 0
	if worktree.Branch != "" {
		checkoutStates++
	}
	if worktree.Detached {
		checkoutStates++
	}
	if worktree.Bare {
		checkoutStates++
	}
	if checkoutStates != 1 {
		return Worktree{}, fmt.Errorf("exactly one branch, detached, or bare field is required")
	}

	return worktree, nil
}

func isGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
