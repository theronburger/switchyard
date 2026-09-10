---
name: switchyard
description: Discover, configure, run, stop, create and prune local development worktrees through Switchyard. Setup runs automatically when starting services.
---

# Switchyard

Use Switchyard for local app instances and Git worktrees of configured repositories.
The native app starts and repairs its helper. CLI and MCP use that same helper.
Configuration belongs outside consuming repositories.

## Work on the right directory

Resolve the task's physical absolute working directory, then call
`switchyard_context(worktreePath)` with that path. The result contains the exact
workspace and its repository's available services and targets. Never choose a
workspace by branch name, list position, or another task's most recent activity.

Git worktrees are immediately usable. There is no adoption or ownership step.
Use `switchyard_inventory` only when deliberately looking across workspaces.

## Run and diagnose

- `switchyard_start` takes `worktreePath`, optional `serviceIds`, `targetId`,
  `requestId` and `confirmedTargetId`. Setup and service preparation happen in
  this action. Do not manually pre-run setup or guess that dependencies exist.
- A returned run is an accepted request, not proof of readiness. Poll context
  until that exact run ID is `running` or `failed`. A replaced run is not success.
- If the selected target has `confirm: true`, get the user's confirmation for
  that target and supply `confirmedTargetId`. Earlier session authorization for
  the same target remains valid. Do not infer approval from configuration alone.
- If a request times out, read context before retrying. Reuse its request ID for
  an identical retry. Use a new request ID for a deliberate new run.
- `switchyard_logs` gives bounded redacted output for this workspace's current
  run. Read its error and logs before changing commands. Do not dump credentials,
  full environments, account information, or agent transcripts.
- `switchyard_stop` cancels setup/start or stops this workspace's services.
  Other workspaces keep running. A helper restart stops runs; start again normally.
- `switchyard_prepare` runs repository setup alone. Success is the same run ID
  with state `stopped`, step `Prepared`, and no error.
- `switchyard_configure` saves the workspace's target and service choices.
  Open the private configuration from the app’s Repositories screen. Valid external saves apply automatically.

Verify URLs directly when claiming the app works. Do not equate a process PID
with a functional app. Investigate missing ignored local files in the private
setup recipe, without editing a consuming repository's tracked files or helpers.

## Create and remove

`switchyard_create_worktree` accepts repository ID, branch and optional base.
It creates an ordinary Git worktree; use its returned path immediately.

For user-requested removal, call `switchyard_prune_plan` first and review its exact
path and blockers. Then call `switchyard_prune_worktree` for that same path.
Switchyard rechecks dirty, unpushed, locked, primary and running worktrees. Never
bypass a blocker with reset, clean, force removal, process-name kills, or global
Docker prune. Unrelated resources must survive.

## CLI fallback

The bundled helper also provides:

```
sy status [PATH] [--all] --json
sy run [PATH] [SERVICE…] [--target TARGET] --wait --json
sy prepare [PATH] --wait --json
sy stop [PATH] --wait --json
sy logs [PATH]
sy prune-plan PATH --json
sy prune PATH --json
sy create REPOSITORY_ID BRANCH [--base REF] --json
sy open [PATH]
sy doctor --json
```

Use `--root` only to select an explicitly known installation. Development and
release roots are separate. If the helper is unavailable, open Switchyard and
use Setup → Repair; do not make the user start a daemon in Terminal.
