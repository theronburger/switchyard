package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"unicode"
	"unicode/utf8"
)

const systemOpenExecutable = "/usr/bin/open"

func (a Application) openWorktree(
	ctx context.Context,
	command parsedCommand,
	stdout, stderr io.Writer,
) int {
	snapshot, err := a.Backend.Status(ctx)
	if err != nil {
		return writeFailure(stdout, stderr, false, err)
	}
	worktreeContext, _, err := a.resolveStatusContext(snapshot, command)
	if err != nil {
		return writeStatusSelectionFailure(stdout, stderr, false, err)
	}
	worktreeURL, err := switchyardWorktreeURL(worktreeContext.Worktree.ID)
	if err != nil {
		return writeFailure(stdout, stderr, false, err)
	}
	launch := a.LaunchURL
	if launch == nil {
		launch = launchSystemURL
	}
	if err := launch(ctx, worktreeURL); err != nil {
		return writeFailure(stdout, stderr, false, err)
	}
	_, _ = fmt.Fprintln(stdout, "Opened Switchyard worktree.")
	return ExitSuccess
}

func switchyardWorktreeURL(worktreeID string) (string, error) {
	if !validWorktreeURLID(worktreeID) {
		return "", fmt.Errorf("invalid worktree identifier")
	}
	escapedID := url.PathEscape(worktreeID)
	worktreeURL := url.URL{
		Scheme:  "switchyard",
		Host:    "worktrees",
		Path:    "/" + worktreeID,
		RawPath: "/" + escapedID,
	}
	candidate := worktreeURL.String()
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme != "switchyard" || parsed.Host != "worktrees" ||
		parsed.EscapedPath() != "/"+escapedID || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid Switchyard worktree URL")
	}
	return candidate, nil
}

func validWorktreeURLID(worktreeID string) bool {
	if worktreeID == "" || len(worktreeID) > 256 || strings.TrimSpace(worktreeID) != worktreeID ||
		!utf8.ValidString(worktreeID) {
		return false
	}
	for _, character := range worktreeID {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func launchSystemURL(ctx context.Context, worktreeURL string) error {
	return systemOpenCommand(ctx, worktreeURL).Run()
}

func systemOpenCommand(ctx context.Context, worktreeURL string) *exec.Cmd {
	return exec.CommandContext(ctx, systemOpenExecutable, worktreeURL)
}
