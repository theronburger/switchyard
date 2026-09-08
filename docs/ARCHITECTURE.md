# Architecture

Switchyard runs configured services in multiple Git worktrees. One Go helper
executes commands and keeps current run state. The native app, CLI and MCP are
clients of its authenticated loopback HTTP API.

```text
Native app ────────┐
sy CLI ───────────┼── local API v3 ── helper ── setup and service processes
Codex / Claude ───┘                      └── Git worktrees and private config
```

## Code boundaries

| Path | Responsibility |
| --- | --- |
| `app/` | Native workspace UI, choices, logs, installation, connections and updates |
| `cmd/switchyard/` | Helper entry point, CLI and MCP request handling |
| `internal/workspaces/` | Configuration, Git operations, run execution, ports, process supervision and logs |
| `internal/localapi/` | Authenticated HTTP server and client |
| `contracts/v3/` | Shared JSON fixtures and API documentation |

`internal/workspaces/types.go` defines the public JSON types. Repository-specific
commands exist only in private configuration outside consuming repositories.
The engine executes those commands directly. MCP and the app contain no separate
execution or Git lifecycle.

## Configuration and workspace identity

`config.json` contains repositories, targets, setup commands, services and saved
workspace choices. Saving validates references and command directories, then
replaces the private file atomically. Future runs use the saved configuration;
an active run keeps its accepted inputs. The file format is version 1 and the
HTTP API is version 3.

Git supplies registered worktrees through `git worktree list --porcelain -z`.
The physical directory identifies a workspace across clients. Branch names and
directory basenames are display information. Context reads resolve child
directories through Git before matching the exact registered root, so a nested
repository is not attributed to an outer workspace.

Discovery reports missing directories and Git inspection failures explicitly.
Prune verifies the exact registration, repository identity, local changes,
remote commit reachability and lock status. The engine adds an active-run
blocker. Removal repeats those checks and calls `git worktree remove` for one
directory without force. Creation uses ordinary Git worktree/branch commands.

## A run

Each workspace has at most one active run. Actions on that workspace serialize;
unrelated workspaces can run concurrently. A run has one ID, state, current
step, start time, selected services, service results, URLs and bounded logs.
Clients track that exact run instead of inferring completion from a previous
healthy instance.

Run performs these steps:

1. Resolve the configured workspace, target and selected services, including dependencies.
2. Save the workspace choices and allocate available local ports.
3. Execute repository setup, then service preparation commands.
4. Start services in dependency order and check their configured readiness.
5. Report running services and URLs; watch for exits and readiness failures.

Setup-only preparation ends after the repository setup commands. It reports
`stopped` with the step `Prepared`. Ordinary run states are `starting`, `running`,
`stopping`, `stopped` and `failed`. Stop cancels preparation or startup as well
as running services. Failure stops the processes launched for that run and
preserves its error and logs in the current helper instance.

Ports are allocated per run. Preferred numbers are checked against local
listeners and other Switchyard runs. Commands receive assigned ports through
environment values and `SWITCHYARD_PORTS` JSON. Workspace paths and port values
are never interpolated into script text. Configuration details are in
[CONFIGURATION.md](CONFIGURATION.md).

## Process lifetime and restart

A small child supervisor leads each command's process group. The helper sends
the script and environment through a pipe. Closing that pipe, losing the helper,
or finishing the command makes the supervisor stop its group. It remains the
group leader during signalling so that the group identifier cannot be reused
for a foreign process. Commands must remain in the foreground and retain their
process group.

Closing the app does not close the helper. Restarting the helper stops its runs
and creates a new API `instanceId`. Current run state and bounded logs are kept
in memory; configuration and choices survive on disk. The replacement helper
does not adopt processes or reconstruct a previous run.

## Local API and app installation

The helper listens on an ephemeral IPv4 loopback port. A descriptor in
`daemon/runtime.json` publishes its endpoint; a separate private `daemon/token`
authenticates clients. Requests declare `X-Switchyard-Version: 3`. A mismatched
client receives an upgrade error. Browser-origin requests are rejected.

The app installs, starts and repairs its bundled helper and agent connections.
Sparkle handles signed app updates. Development uses a separate support root
and connection name. The [API contract](../contracts/v3/README.md),
[safety rules](SAFETY.md), and [release runbook](RELEASING.md) describe these
boundaries in more detail.
