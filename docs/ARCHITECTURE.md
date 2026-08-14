# Architecture

## System boundary

Switchyard is a machine-local control plane. It coordinates developer resources already authorized by the user: Git worktrees, native service processes, local ports, Colima/Docker resources, logs, and local agent sessions.

```text
Codex / Claude ── MCP ──┐
                        │
Human shell ──── CLI ───┼── local versioned API ── Go daemon
                        │                         ├── repository adapters
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

The app should render useful fixture data before the daemon exists so UI work can proceed independently.

### Go helper

Prefer one bundled Go executable with modes or subcommands for:

- daemon;
- CLI client;
- MCP stdio server;
- installation/doctor utilities when the app invokes them.

The MCP process is per client and therefore stateless. It connects to the machine daemon. If an installed daemon is absent, the helper may ask launchd to start it. If setup or approval is required, it returns a useful repair error without launching the GUI.

### Core domain

The core knows these generic concepts:

- `Repository`
- `Worktree`
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

It does not know Deed environment variable names or Turbo commands. Those belong to the Marketplace adapter.

### Repository adapters

An adapter translates repository-specific reality into the core:

- discovery and identity;
- affected-service calculation;
- service definitions and commands;
- routing environment generation;
- infrastructure declarations;
- health checks;
- worktree bootstrap hints;
- integration/union provenance.

Marketplace is built in first. The boundary should be a small Go interface plus a schema-versioned local manifest, not a runtime plugin marketplace.

## State and identity

State lives under `~/Library/Application Support/Switchyard/` unless the final name changes. SQLite is authoritative; generated files are projections.

Stable identity must not depend only on directory basename or branch name:

- repository identity derives from the Git common directory and remote;
- worktree identity derives from the repository plus Git's administrative worktree identity;
- environment identity survives a branch rename but records branch history;
- a process identity includes PID and start time to defend against PID reuse;
- Docker resources carry Switchyard, repository, environment, and run labels.

## Local API

Use a small versioned JSON contract. Unix-domain socket versus authenticated loopback HTTP remains an implementation decision; the contract must not depend on transport.

V1 uses authenticated loopback HTTP on an ephemeral port described by a mode-`0600` runtime file. A separate mode-`0600` bearer token authenticates all clients. Required qualities:

- request IDs and idempotency keys for mutations;
- explicit desired and observed states;
- streaming or tail-friendly logs/events without making the UI poll aggressively;
- an exact-version handshake with a stable upgrade-required error;
- atomic status snapshots;
- stable machine-readable error codes;
- no secret values in responses.

## State footer

Every environment-scoped MCP tool result includes a complete capped environment context at a revision. It informs the agent at action boundaries without sending unsolicited messages or requiring per-client cursor state. Global calls have no implicit environment.

Illustrative shape:

```json
{
  "result": {},
  "environmentContext": {
    "revision": 42,
    "environmentId": "env_01J5EXAMPLE",
    "health": "degraded",
    "urls": {
      "organizer": "http://localhost:7005"
    },
    "attention": [
      {
        "code": "SERVICE_CRASHED",
        "summary": "nonprofit-service exited with status 1"
      }
    ]
  }
}
```

Cap attention at three entries and URLs at eight. The UI owns proactive notification. MCP never wakes or interrupts an agent.

## Service lifecycle

1. Resolve or create the environment.
2. Ask the adapter for a service plan.
3. Acquire stable leases after checking both daemon state and the operating system.
4. Materialize the environment projection.
5. Ensure infrastructure with explicit sharing scope.
6. Start each logical service in an owned process group.
7. Launch children with stdout/stderr directed to owned rotating files; the daemon tails files so logs survive daemon restarts.
8. Evaluate readiness and health separately from process liveness.
9. Reconcile desired and observed state after app, daemon, Colima, or service restarts.
10. Stop with TERM, a grace period, then revalidate every group member's PID, start time, and fingerprint before KILL is allowed.

Every mutation is first persisted as an asynchronous operation. Operations are serialized per environment so unrelated environments progress concurrently. Reconciliation resumes or safely fails incomplete operations after a daemon restart.

Retries use bounded exponential backoff. Crash loops become an alert rather than an infinite silent restart.

## Monitoring cadence

- Owned process exits and Docker events: event-driven.
- Health checks: frequent while starting, relaxed when stable.
- Git/worktree state: on demand plus modest periodic reconciliation.
- Agent-session discovery: consented and adaptive, inspired by CodexBar.
- CPU and memory: grouped by logical service, sampled at a modest cadence.
- Disk accounting: slow background work, cached and paused under resource pressure.
- PR/CI and integration provenance: optional polling with backoff.

Low Power Mode and serious/critical thermal pressure should reduce nonessential scans.
