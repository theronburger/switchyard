# Safety invariants

Switchyard manages processes, worktrees, ports, and Docker resources. Safety is a product feature, not a cleanup phase.

## Ownership

- Start every managed service in a distinct owned process group.
- Persist PID, process start time, environment ID, run ID, command fingerprint, and log paths.
- Persist process-group membership and revalidate every member immediately before signalling; PID and group identifiers can both be reused.
- A non-leader whose live argv fingerprint changes may be requalified only when PID, process-group ID, and start time still match and its current parent chain reaches an exactly verified owned member. Persist the refreshed fingerprint. Leader drift, detached children, or instability across the two pre-signal scans remain report-only.
- Never kill by executable name, fuzzy command match, port alone, or ancestry guess.
- Unknown matching processes are report-only until explicitly adopted.
- Reconciliation after restart must defend against PID reuse.

## Ports

- The daemon is the sole Switchyard lease allocator.
- A lease is stable for an environment/service pair when possible.
- Check the operating system before allocation and immediately before launch.
- A foreign listener produces a conflict and an alternative plan; it is never killed automatically.
- Failed launches release or quarantine leases deliberately rather than leaking them.

## Docker and Colima

- Put tool, repository, environment, and run labels in every create specification so ownership is atomic with resource creation.
- Use a unique Compose project name per isolated environment.
- Consume Docker events for lifecycle, health, and OOM signals.
- Never invoke a global `docker system prune` automatically.
- Foreign dangling resources may be measured and reported but not removed.
- Cleanup of owned resources still starts with a plan.
- Detect Docker context and Colima socket/profile; do not assume Docker Desktop paths.

## Worktrees

- Parse `git worktree list --porcelain -z` as the source of registered worktrees.
- Respect Git's locked and prunable states.
- Never remove a dirty, untracked-sensitive, unpushed, active, or agent-occupied worktree.
- A cleanup plan must explain every protection and estimated reclaimed size.
- Require an explicit apply operation against a still-valid plan revision.
- Revalidate immediately before mutation.

## Cleanup protocol

All material cleanup is two-phase:

1. `plan`: read-only inventory, protections, targets, estimated benefit, and plan revision;
2. `apply`: exact target IDs, revision check, revalidation, action, and audit event.

Plans expire when relevant state changes. The UI must make destructive scope legible.

## Configuration and hooks

- Personal configuration may contain absolute paths but no secrets.
- Shared local exclude edits are idempotent, marker-delimited, append-only, and preserve existing content. This is Switchyard's only permitted write under Marketplace's common `.git` directory.
- Never source an environment file as shell code merely to read values.
- Repository-provided commands discovered from configuration require an inspectable plan before first execution.
- Do not mutate tracked Marketplace files.
- Do not write public ignore rules for a personal tool.
- Codex and Claude MCP mutations go through each host's exact CLI. Switchyard bounds and validates owner-controlled configuration before repair, serializes mutations, and uses compare-and-swap restoration that never overwrites a concurrent change.

## Privacy

- Agent-session detection requires explicit consent before scanning process command lines or transcript metadata.
- Retain the minimum useful session metadata.
- Do not store transcript contents.
- Redact secrets and URI credentials from logs and child output.
- Do not expose full process commands through the app, CLI JSON, or MCP.
- Bind local APIs to a protected Unix socket or authenticated loopback interface.

## Alerts

Alerts must be actionable and deduplicated. Repeated unchanged state should not train the user to ignore the app.

Initial alert classes:

- service exited or crash-looped;
- health degraded after readiness;
- port lease stolen or foreign conflict discovered;
- unexpected orphan from an owned run;
- owned Docker resource unhealthy/OOM/dangling;
- Colima/Docker unavailable while required;
- dirty or unpushed worktree proposed for cleanup;
- integration/union provenance stale;
- sustained logical-service CPU or memory pressure;
- disk pressure or unusually reclaimable artifacts.

The app may notify the user. Agents receive current alert context only in responses to their own actions.
