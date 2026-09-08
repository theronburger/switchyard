# Execution rebuild

The agreed product retains worktree discovery and pruning, private configuration,
agent interactions, the native UI, installation, signed releases and automatic
updates. The execution and state implementation is new code. The previous engine
is not imported or adapted into it. Fable is excluded for this task.

## Product model

- Git supplies workspace directories and branches. There is no ownership,
  adoption, occupancy lease, or private worktree registration.
- A repository has ordinary setup and service commands. Each workspace remembers
  its target and selected services. Save is configuration acceptance.
- Run executes setup, service preparation, then services and readiness checks.
  Repository tools own dependency caching. Switchyard does not maintain another
  readiness fingerprint or setup journal.
- Services have named ports and dependencies. Each running workspace gets its
  own ports. Commands receive these ports and local URLs before they start.
- A run has one ID, one state, current step, start time, service results, and
  bounded logs. Clients use this same state. The daemon serializes actions only
  within the affected workspace.
- Stop signals only processes launched for that run. Small child supervisors
  stop their process groups if the daemon disappears. There is no process
  adoption, persisted ownership graph, or reconciliation framework.
- Prune previews the exact directory and concrete Git blockers, checks again,
  and calls Git without force. It never resets, cleans, or prunes Docker globally.
- App closure leaves the daemon running. A daemon restart stops its runs; the
  app reports stopped and offers Run. Setup choices survive restarts.

## Implementation

The replacement is now the product implementation: `cmd/switchyard`,
`internal/workspaces`, `internal/localapi` and `app`. It imports none of the
previous engine. API v3 fixtures are in `contracts/v3`.

The previous source is preserved in Git history and in a local backup made before
this task. It is absent from the product build. Existing release scripts, signing
identities, Sparkle configuration, icons and release bundle identity are retained.
Development uses a separate support root.

## Frozen interface

`internal/workspaces/types.go` is the Go/JSON source of truth. HTTP requests use
`/api/status`, `/api/config`, `/api/run`, `/api/prepare`, `/api/stop`,
`/api/logs?path=...`, `/api/prune-plan?path=...`, `/api/prune`, and `/api/create`.
Commands are private shell scripts with a relative working directory and timeout.
Their environment can use `{workspace}`, `{repository}`, `{instance}`, and
`{port:service.port}` substitutions. Port substitution is environment-only;
workspace paths are never interpolated into executable shell text.

HTTP mutations identify physical workspace paths. Run requests carry an opaque
request ID, target, selected service IDs and optional confirmed target. Reusing
the same request ID returns the same run. Errors are `{error: "plain reason"}`.
No credentials or raw command lines appear in status or logs returned to clients.

## Acceptance

Use fresh disposable repositories and real processes, including HTTP and a
forking service. Verify two simultaneous workspaces, setup failure, service exit,
readiness failure, cancellation, retry, app restart, daemon termination, foreign
port collision, and stopping one while the other remains reachable. Verify
pruning leaves dirty, unpushed, primary, locked, active and unrelated worktrees
intact. Exercise CLI and MCP against the same daemon. Build and visually inspect
the real native app, then verify packaging and updates remain intact.

An actual configured repository also needs setup/start verification. Synthetic
success does not prove that private repository recipe is complete.
