# Architecture

## System boundary

Switchyard is a machine-local control plane. It coordinates developer resources already authorized by the user: Git worktrees, native service processes, local ports, Colima/Docker resources, logs, and local agent sessions.

```text
Codex / Claude ── MCP ──┐
                        │
Human shell ──── CLI ───┼── local versioned API ── Go daemon
                        │                         ├── accepted repository profiles
SwiftUI app ────────────┘                         ├── leases and supervisor
                                                  ├── health and reconciliation
                                                  ├── Docker/Colima observer
                                                  └── SQLite and event history
```

The daemon is the sole authority. Every mutation is serialized through it. A client can disappear without orphaning ownership decisions or corrupting shared state.

## Components

### SwiftUI app

The app is the primary human experience:

- menu bar summary of running, unhealthy, and attention-needed environments;
- full command-center window for worktrees, services, logs, disk, Docker, events, and cleanup plans;
- launch-at-login behavior;
- bundled helper installation and version reconciliation;
- one-click Connection Doctor and Repair All;
- Codex and Claude MCP configuration and validation;
- notifications with direct actions such as Open Logs, Restart, Stop, or Review Plan.
- per-service committed and uncommitted line attribution, with shared repository changes kept separate;
- exact-worktree editor actions such as opening the checkout in a new Zed window;
- branch-derived Jira references with optional app-owned reads through the raw relay contract, isolated from daemon health;
- daemon-owned GitHub pull-request and CI observations through the user's authenticated Keychain-backed `gh` session, isolated from environment health;
- running state rendered directly in the menu-bar mark, with configurable compact attention and resource indicators beside it.

The app should render useful fixture data before the daemon exists so UI work can proceed independently.

### Go helper

Prefer one bundled Go executable with modes or subcommands for:

- daemon;
- CLI client;
- MCP stdio server;
- installation/doctor utilities when the app invokes them.

The MCP process is per client and therefore stateless. It connects to the machine daemon. If an installed daemon is absent, the helper may ask launchd to start it. If setup or approval is required, it returns a useful repair error without launching the GUI.

MCP process working directories are not identity. A host can share or launch a server outside the active task, so the current-worktree read requires the physical absolute workspace path from host context; clients retry a rejected logical hint with read-only `pwd -P`, never branch guessing. The resolver canonicalizes existing symlinks before matching. Global inventory and single-environment polling are separate tools. The CLI may safely derive the same worktree projection from its own process working directory; `--all` opts into inventory.

### Core domain

The core knows these generic concepts:

- `Repository`
- `Worktree`
- `WorkspacePlan`
- `WorkspaceReadiness`
- `Toolchain`
- `Environment`
- `ServiceDefinition`
- `ServiceRun`
- `ProcessGroup`
- `PortLease`
- `InfrastructureLease`
- `DockerResource`
- `HealthObservation`
- `Alert`
- `CleanupPlan`
- `AgentSession`
- `Event`

It does not know package managers, language runtimes, repository-specific environment variable names, or build orchestrators. The profile compiler turns an accepted private repository profile into exact finite workspace steps, generic toolchain metadata, requirements, and environment plans.

### Workspace before environment

Every environment start passes through `workspace.Ensure` first. The coordinator serializes per worktree, computes a profile-defined content fingerprint, re-verifies readiness requirements on cache hits, durably checkpoints each finite step, and publishes a repository-neutral readiness result to SQLite and the status snapshot. Profiles can fingerprint toolchain files, lockfiles, and bounded package manifests while excluding generated dependency trees. Shared content-addressed caches remain available while each worktree keeps its own mutable install tree.

Agent occupancy is explicit. When the app launches a task into a worktree it first records one conservative handoff lease through the daemon (`occupancy`), with a generic holder kind and a bounded label, and only then opens the task; a refused lease means no launch, a failed launch releases the lease it acquired, and a failed rollback keeps the protective lease and tells the owner where to release it. The daemon never infers occupancy from a deep link, process, or transcript, never expires a lease, and only an owner release ends it. Held leases are durable, survive restarts and inventory rebuilds, and make archive refuse with `WORKTREE_OCCUPIED`.

Git creation/adoption/removal is a separate positively-owned lifecycle. Existing worktrees are auto-discovered with run-only `adopted` inventory ownership. An explicit adoption may promote an eligible non-primary checkout to `managed` only when it is a clean, pushed, non-symlinked direct child of the configured managed root and Git proves it belongs to the exact repository. Switchyard-managed worktrees have a durable private ownership record bound to repository root, exact worktree path, branch, upstream/start revision, and Git administrative directory. Archive re-verifies that identity and refuses primary, active, dirty, unpushed, foreign, or unverifiable worktrees.

### Repository profiles

A private repository profile translates repository-specific reality into generic control-plane inputs:

- discovery and identity;
- affected-service calculation;
- service definitions and commands;
- routing environment generation;
- infrastructure declarations;
- health checks;
- worktree bootstrap hints;
- integration/union provenance.

Profiles are data, not compiled adapters. Adding or changing a consuming repository must not require product code changes. Generic capabilities belong in the control plane only when they can be described, validated, planned, and tested without repository identity.

## State and identity

State lives under `~/Library/Application Support/Switchyard/` unless the final name changes. SQLite is authoritative; generated files are projections.

Durable state is bounded and pinned:

- the atomic status snapshot is one row with a monotonic revision; history is never retained merely to update a timestamp;
- event history keeps the most recent 10,000 audit events (`operation.created`, `operation.transitioned`, `configuration.accepted`, `occupancy.acquired`, `occupancy.released`, `cleanup.applied`), each appended in the transaction that made the change;
- terminal operations keep the most recent 500 unless a current environment or workspace result still references them; incomplete operations are never pruned;
- accepted configuration revisions keep the head, the most recent 16 revisions, and every revision whose repository digest is pinned by a live (non-stopped) environment result or an incomplete environment operation; staged-but-unaccepted candidates keep the most recent 16;
- every environment result and operation intent records the accepted repository-profile digest it was compiled from, so a restart after a later acceptance recovers the exact payload; a live result or incomplete operation whose pinned payload is no longer retained, or whose environment no longer belongs to an accepted profile, fails boot closed rather than silently re-reading the head. A stopped result is finished history: it pins nothing and never blocks boot, so archiving a worktree or disabling a repository after its environments have stopped keeps the daemon bootable.

Repository identity in the public contract is the private `profileKey`; the adapter concept does not exist in contract v2 or in persisted 0.2.0 state. Switchyard 0.2.0 is a clean cutover from 0.1.x: it opens a fresh `state-v2.sqlite` and carries no reader, importer, or migration for 0.1.x state, operations, or runtime ownership. A single-user install removes or archives the 0.1.x Application Support contents and starts fresh.

Stable identity must not depend only on directory basename or branch name:

- repository identity derives from the Git common directory and remote;
- worktree identity derives from the repository plus Git's administrative worktree identity;
- environment identity survives a branch rename but records branch history;
- a process identity includes PID and start time to defend against PID reuse;
- Docker resources carry Switchyard, repository, environment, and run labels.

## Local API

Use a small versioned JSON contract (`contracts/v2`, `schemaVersion: 2`). Unix-domain socket versus authenticated loopback HTTP remains an implementation decision; the contract must not depend on transport.

V1 uses authenticated loopback HTTP on an ephemeral port described by a mode-`0600` runtime file. A separate mode-`0600` bearer token authenticates all clients. Required qualities:

- request IDs and idempotency keys for mutations;
- explicit desired and observed states;
- tail-friendly logs/events without making the UI poll aggressively or placing raw log contents in routine agent context;
- an exact-version handshake with a stable upgrade-required error: every versioned request declares `X-Switchyard-Schema-Version`, and a mismatch is HTTP 426 `UPGRADE_REQUIRED`;
- atomic status snapshots;
- stable machine-readable error codes;
- no secret values in responses.

## Context reads and state footer

The daemon's atomic snapshot is global, but client reads are intentionally explicit. Human `status` resolves the containing worktree and `status --all` requests inventory. MCP exposes `switchyard_context(worktreePath)`, `switchyard_environment_status(environmentId)`, and `switchyard_inventory()` instead of an ambiguous generic status tool. The context read returns one repository/worktree plus only its environments, operations, and alerts; the environment read returns one exact environment and its owning worktree. Inventory never implies that its first or similarly named entry is current.

Failed operations keep routine reads compact by publishing structured, bounded failure context and an opaque log reference rather than log contents. When that diagnosis is insufficient, `switchyard_operation_diagnostics(operationId, maxBytes?)` explicitly retrieves bounded tail excerpts from the referenced Switchyard-owned stdout/stderr files. The daemon, not MCP, resolves and validates the reference, rejects links or unsafe file modes, caps each stream, and applies command, environment, credential, account-path, and email safety redactions before content crosses the API boundary.

Every accepted environment mutation result also includes a complete capped environment context at a revision. It informs the agent at action boundaries without sending unsolicited messages or requiring per-client cursor state. Workspace mutations return their receipt and deliberately restart the helper after successful identity-changing actions.

Illustrative shape:

```json
{
  "result": {},
  "environmentContext": {
    "revision": 42,
    "environmentId": "env_01J5EXAMPLE",
    "health": "degraded",
    "urls": {
      "storefront": "http://localhost:7005"
    },
    "attention": [
      {
        "code": "SERVICE_CRASHED",
        "summary": "billing-service exited with status 1"
      }
    ]
  }
}
```

Cap attention at three entries and URLs at eight. The UI owns proactive notification. MCP never wakes or interrupts an agent.

## Service lifecycle

1. Resolve or create the environment.
2. Compile the service plan from the accepted repository profile pinned by the operation's `ProfileDigest`.
3. Acquire stable leases after checking both daemon state and the operating system.
4. Materialize the environment projection.
5. Ensure infrastructure with explicit sharing scope.
6. Start each logical service in an owned process group.
7. Launch children with stdout/stderr directed to owned rotating files; the daemon tails files so logs survive daemon restarts.
8. Evaluate readiness and health separately from process liveness.
9. Reconcile desired and observed state after app, daemon, Colima, or service restarts.
10. Stop with TERM, a grace period, then revalidate every group member's PID, start time, and fingerprint before KILL is allowed.

Every mutation is first persisted as an asynchronous operation. Operations are serialized per environment so unrelated environments progress concurrently. Reconciliation resumes or safely fails incomplete operations after a daemon restart.

Operations that are not environment-scoped are serialized by narrow resource keys shared across the workspace, environment, and profile-action services. Everything that mutates one worktree holds its worktree key: preparation, archive, adoption, an environment start from its workspace ensure until the coordinator publishes the environment, and worktree-, environment-, or service-scoped command actions. Everything that mutates a repository's shared Git administrative state holds its repository key: creation, archive, adoption, and repository- or machine-scoped command actions. A conflicting operation is accepted immediately but stays `pending` until the keys are free; keys are taken all-or-nothing so a queued archive never stalls unrelated worktrees through the repository key, and an operation still waiting when the daemon shuts down fails as interrupted without ever having run. Unrelated worktrees and repositories keep running concurrently.

Finite command actions launch through the same process-ownership host as services: a launch intent is persisted before fork and the verified leader identity immediately after, in the action's private run directory under `runtime/actions/<profileKey>/<operationId>`. A daemon that dies mid-action therefore leaves positively verifiable evidence. On every boot, before any profile is consulted, the daemon stops each action group whose record is still running through the verified TERM, grace, KILL sequence, then fails the interrupted operation with `DAEMON_RESTARTED`; evidence that cannot be verified (an intent without ownership, a malformed record, a live identity that no longer matches) is counted and left alone. A finished action ends with its whole group stopped, including descendants that outlived the leader.

Workspace preparation steps and environment initialization commands are owned the same way. Because a step's own run directory is exactly what the private-preparation cleanup planner identifies (its `ownership.json` marker and bounded logs), their process evidence lives in a separate flat tree, `runtime/preparation-runs/<launch>`, one private directory per launch; the step or initialization run directory keeps only its marker and logs. Boot stops every launch whose record is still running before the interrupted preparation or start is failed, leaves unverifiable evidence in place, and a launch whose group is verified stopped removes its own evidence, so the tree only ever holds launches a restart must still act on or report.

Immediately before a start is accepted, the daemon reads the exact worktree HEAD and tracked/untracked dirty state. That source snapshot and the newly allocated run ID are persisted with the operation and projected into every service run. A terminal operation is not evidence that an older healthy run was replaced unless the published environment carries the same run ID.

Retries use bounded exponential backoff. Crash loops become an alert rather than an infinite silent restart.

## Monitoring cadence

- Owned process exits and Docker events: event-driven.
- Health checks: frequent while starting, relaxed when stable.
- Git/worktree state: at startup and every 30 seconds, with per-repository successful-observation and last-attempt timestamps; failed refreshes retain explicitly stale last-known data.
- Agent-session discovery: consented and adaptive, inspired by CodexBar.
- CPU and memory: grouped by logical service, sampled at a modest cadence.
- Disk accounting: slow background work, cached and paused under resource pressure.
- PR/CI: immediate for unseen or changed heads; 30 seconds while checks are pending; then stepped from one minute to six hours as PR activity ages. Branches with no PR receive an hourly safety lookup. Failures retry every five minutes while preserving last-known state.

Low Power Mode and serious/critical thermal pressure should reduce nonessential scans.
