# Safety invariants

Switchyard manages processes, worktrees, ports, and Docker resources. Safety is a product feature, not a cleanup phase.

## Ownership

- Start every managed service in a distinct owned process group.
- Persist PID, process start time, environment ID, run ID, command fingerprint, and log paths.
- Persist process-group membership and revalidate every member immediately before signalling; PID and group identifiers can both be reused.
- A non-leader whose live argv fingerprint changes may be requalified only when PID, process-group ID, and start time still match and its current parent chain reaches an exactly verified owned member. Persist the refreshed fingerprint. Leader drift, detached children, or instability across the two pre-signal scans remain report-only.
- The one exception is the daemon's own unreaped child. Exit is observed before reaping, and reaping and signalling both happen under the run lock, so while the daemon holds that lock and has not reaped the leader, the leader's PID and therefore its group ID provably denote this run. Only then may the leader's fingerprint be requalified (a shell shim or `env` shebang replaces the executable image during startup) and may group members that verification cannot tie to a persisted ancestor still be signalled. A restarted daemon is never the parent and keeps the strict rule.
- Finite command actions persist the same launch intent and verified ownership as services. Boot stops every action group left running by the previous daemon through verified ownership, counts unverifiable evidence without signalling it, and a finished action's exited leader is kept unreaped until descendants that outlived it have been stopped through the same ownership.
- Workspace preparation steps and environment initialization commands persist the same evidence in `runtime/preparation-runs`, separate from the step directories the cleanup planner identifies by marker. Boot recovers them with the same verified stop, report-only rule for drifted, intent-only, malformed, or foreign records, and evidence is removed only once a record proves its group finished.
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

- Operations on one worktree (prepare, archive, adopt, an environment start, worktree-bound command actions) and on one repository's shared Git state (create, archive, adopt, repository-scoped command actions) run one at a time; a conflicting operation stays pending rather than racing. Unrelated worktrees remain concurrent.
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

Apply is a claimed transaction with a fixed order:

1. read-only revalidation inputs are gathered;
2. authorization for exactly this plan revision and candidate list is claimed atomically in durable state, before any owned resource is touched — a request that loses the claim mutates nothing;
3. each candidate is revalidated and removed in request order, and every outcome is journaled before the next candidate starts; the candidate currently being removed is recorded as in flight;
4. completion records the result, consumes the plan, and appends the `cleanup.applied` audit event in one transaction; an interrupted apply has no completion event and a replay never adds a second one.

A second apply of the same plan — concurrent, from another daemon process, or with a different candidate list — is refused (`CLEANUP_APPLY_IN_PROGRESS`, `CLEANUP_APPLY_MISMATCH`). Repeating the identical request after an interruption resumes the same claim: already-final outcomes are replayed from the journal, the in-flight candidate is attempted again, and the attempt count is reported. An in-flight candidate that no longer matches its plan identity, or whose removal failed part-way, is reported as `interrupted`, never as removed; its remaining files are still positively owned and re-plannable because removal deletes logs before the ownership marker. Repeating an identical request after completion replays the recorded result. An incomplete claim pins its plan against pruning even after the plan expires.

## Configuration and hooks

- Personal configuration may contain absolute paths but no secrets.
- Switchyard configuration and ownership markers stay outside consuming repositories. No tracked file, public ignore file, or shared Git metadata is mutated.
- Never source an environment file as shell code merely to read values.
- Repository-provided commands discovered from configuration require an inspectable plan before first execution.
- Do not mutate tracked files in consuming repositories.
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
