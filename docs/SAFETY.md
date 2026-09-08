# Safety rules

Switchyard starts configured commands and removes explicitly selected Git
worktrees. Keep both actions tied to their exact workspace and current run.

## Processes and ports

- Signal only a process group created for a Switchyard command. Never target an executable name, fuzzy command match, port owner or remembered PID.
- Keep the command supervisor alive as its group leader until stopping is complete. Loss of the helper's pipe must stop that command group.
- Stop all commands launched for the failed or cancelled run, including background descendants still in those groups. Leave other runs and foreign processes alone.
- Commands must run in the foreground and must not escape their process group. Switchyard does not adopt detached processes.
- Check preferred ports against the operating system and other active runs. Choose another available port instead of stopping a foreign listener.
- Release a run's port reservations after its processes stop. Service commands must use their assigned ports.
- Never run a global Docker prune. Switchyard has no general-purpose Docker cleanup action.

## Git worktrees

Git registration and physical directory identity determine the target. Names,
branch labels and path prefixes are insufficient.

Prune must show the exact path and its blockers, then repeat the checks before
removing anything. Protect:

- the primary worktree;
- locked worktrees;
- tracked changes and untracked files reported by Git;
- commits not reachable from locally known remote branches;
- workspaces with active setup, startup, running or stopping commands;
- directories whose Git registration or repository identity cannot be verified.

Use `git worktree remove` without force for one exact registered directory.
Do not reset, clean, recursively delete a checkout, or run a global Git worktree
prune. Removing one missing worktree registration must leave other missing and
foreign registrations intact. Ordinary Git creation must not overwrite an
existing branch or directory.

Prune cannot infer whether an editor or agent is using a clean, stopped
workspace. Its preview lets the user review the actual directory before removal.

## Private configuration

Repository commands, paths and environment values belong in private
configuration outside consuming repositories. Save validates the configuration
and applies it for future runs; there is no separate approval compiler.

Do not add product configuration to consuming repositories or change their
tracked files, public ignore rules or existing development helpers. Required
ignored local files must be supplied deliberately by the configured setup
commands. Command working directories must resolve inside the workspace.
Environment substitutions must never rewrite executable shell text.

Configuration is saved atomically with mode `0600`. The explicit configuration
API exposes private command settings to authenticated local clients, so it must
receive the same protection as the file.

## Privacy and local access

- Bind the helper to loopback and require its separate private bearer token. Keep tokens out of URLs and error messages.
- Require the client's exact API version and reject browser-origin requests.
- Keep routine status free of scripts and environment values. Return bounded, redacted output only for explicit log reads.
- Never deliberately print credentials, full command lines, environment dumps, account identifiers or transcript contents.
- Use exact workspace paths for agent context. Do not inspect transcripts to guess which task owns a directory.
- The app may inform the user about failures. Background work must not inject chat messages, wake sleeping agents or interrupt running agents.

## Verification

Exercise failures with real disposable processes and repositories. Prove that
stopping one run leaves another reachable, a helper exit stops its command
groups, and a prune refusal leaves protected and foreign worktrees unchanged.
Passing status fixtures alone does not prove those behaviors.
