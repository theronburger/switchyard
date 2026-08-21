# Contract v2

Contract v2 is the local JSON boundary shared by the Go daemon, CLI, MCP clients, and Swift app. It is the Switchyard 0.2.0 contract and replaces contract v1, which shipped with 0.1.0.

## Changes from v1

Contract v2 changes only what the product contract actually changed when built-in adapters were replaced by private repository profiles:

- `schemaVersion` is the integer `2` on every status, request, receipt, descriptor, and handshake payload. The handshake advertises `supportedSchemaVersions: [2]` only. Every client declares its exact version on every request (see "Exact-version declaration" below); a v1 client, or any request body from another contract generation, receives HTTP 426 with the stable `UPGRADE_REQUIRED` error and the app replaces its bundled daemon before reconnecting.
- `repositories[].profileKey` replaces `repositories[].adapter`. It is the stable private repository key from the accepted configuration, not a display name. The CLI and MCP status projections use the same field name.
- The repository observation failure code `PROFILE_OBSERVATION_INVALID` replaces `ADAPTER_OBSERVATION_INVALID`.
- Every other v1 shape is carried forward unchanged, including mutation requests, receipts, operations, cleanup, configuration, and profile-action payloads. Additive fields remain allowed.

No client-facing payload carries an adapter concept any more. Internally, pinned environment intent identifies the accepted repository-profile digest (`ProfileDigest`) rather than an adapter name, and workspace plans and results carry the `ProfileKey`. Those private records are not part of this contract, but the daemon migrates 0.1.0 state to the new names in place on first start (state migration 10) and refuses to guess when a legacy payload is malformed. Running environments pinned to an older accepted revision keep their exact payload across restarts and later acceptances; the daemon never substitutes the current head for a pinned run.

## Rules

- `schemaVersion` is the integer `2`.
- IDs are opaque strings. Ticket, branch, and path data are display fields.
- timestamps are RFC 3339 strings;
- collection fields are always JSON arrays or objects, never `null`;
- revisions are non-negative signed 64-bit integers and increase monotonically within their scope;
- desired lifecycle, observed lifecycle, and health are separate axes;
- public snapshots never contain credentials, environment values, full commands, PIDs, or process arguments;
- mutations are asynchronous operations with idempotency keys and optional expected environment revisions;
- worktree and environment read projections contain only their selected scope at one snapshot revision;
- accepted environment mutations include their immutable run ID and a complete capped context footer at a revision.

Operations publish their current lifecycle `phase`. Terminal failures carry a stable
error code and safe, bounded context where available: the phase and step, affected
resource, retryability, a concise diagnostic, a machine-readable next action, exit
status, and an opaque `logReference`. Normal status responses deliberately omit log
contents so routine agent reads stay small. A client can explicitly request bounded
tail excerpts from Switchyard-owned logs with
`GET /v1/operations/{operationId}/diagnostics?maxBytes=...`; the daemon enforces
ownership, file-mode, path-containment, and size checks and applies safety redactions.
The equivalent MCP read is `switchyard_operation_diagnostics`.

Lifecycle vocabulary is finite. Desired state is `unknown`, `running`, or `stopped`;
observed state is `unknown`, `stopped`, `starting`, `running`, `stopping`,
`exited`, `failed`, `orphaned`, or service-level `unverifiable`; health is
`unknown`, `not_applicable`, `starting`, `healthy`, `degraded`, or `unhealthy`.
Readers retain compatibility for the previously persisted desired `failed` and
observed `degraded` values, but producers keep failure in observed state and
degradation in health. A service may publish a bounded stable
`observationCode`; it never contains process arguments or private paths.

`fixtures/status.json` is the canonical cross-language decoding fixture. Additive fields must not break clients. Contract changes are coordinator-owned and require the Go fixture test, Swift tests, and the Swift conformance executable.

Every other public shape also has a frozen fixture that both languages decode strictly and validate: `configuration-status.json` and `configuration-repository-mutation-request.json` (private configuration through the daemon), `profile-action-list.json` and `run-profile-action-request.json` (accepted profile actions), `operation-diagnostics.json` (bounded log excerpts), `occupancy-lease.json`, `acquire-occupancy-request.json`, and `release-occupancy-request.json` (worktree handoff leases), and `upgrade-required-error.json` (the exact-version error envelope).

`fixtures/start-environment-request.json`, `fixtures/stop-environment-request.json`, and `fixtures/mutation-receipt.json` freeze the asynchronous environment-action boundary. Starting names a discovered worktree, a repository-configured target such as `testing`, and an explicit non-empty service set. Omitting the target at the CLI/MCP boundary selects the repository default. A target with `warnOnStart` requires `confirmedTargetId` to equal the resolved target for that start request; confirmation is never persisted as reusable authorization. Stopping names the environment in the route. Both carry a request ID, idempotency key, and optional expected environment revision; acceptance returns an operation ID immediately. Start receipts and their public operations also carry the same immutable `runId`, so clients never infer completion from a previously healthy run.

Profile actions are accepted private behavior, never client-side scripts. `GET /v1/actions` lists every action in the accepted configuration revision as `{id, repositoryId, profileKey, profileDigest, displayName, scope, risk, kind, lifecycle?, requiresConfirmation}`; it never exposes an executable, argument, or environment shape. `POST /v1/actions/run` carries a mutation request plus `repositoryId`, `actionId`, and exactly the target identifiers the action's scope requires (`worktreeId` for worktree scope, `environmentId` for environment scope, `environmentId` plus `serviceId` for service scope, none for machine or repository scope); a mismatch is `ACTION_SCOPE_MISMATCH`. `remote-write` actions require `confirmedActionId` to equal `actionId` on every run (`ACTION_CONFIRMATION_REQUIRED`); confirmation is never persisted. Lifecycle actions dispatch the existing prepare, start, and stop operations and return their receipts; `cleanup` answers `ACTION_REQUIRES_REVIEW` because cleanup is always plan then apply. Command actions become a `profile.action` operation pinned to the accepted revision: the idempotency fingerprint includes the profile and configuration digests, a drifted desired configuration fails closed with `CONFIGURATION_NOT_ACCEPTED`, environment-scoped runs serialize with other operations on that environment, worktree- and repository-bound runs stay pending behind conflicting workspace operations on the same worktree or repository, and failures publish only a bounded `ACTION_COMMAND_FAILED`, `ACTION_TIMED_OUT`, `ACTION_COMMAND_INVALID`, `ACTION_COMMAND_START_FAILED`, `ACTION_GROUP_UNVERIFIED`, or `ACTION_INTERRUPTED` error with exit status and an opaque `logReference` into Switchyard-owned bounded output files.

Private configuration is read and edited only through the daemon. `GET /v1/configuration` publishes the accepted revision and digest, any staged candidate, and a bounded `desired` view of `configuration.yaml`: whether it exists, its exact `sourceDigest`, a bounded `problem` when the file cannot be read or compiled, and the generic identity fields of each repository entry (`key`, `enabled`, `displayName`, `root`, `remote`, `defaultBase`, `managedWorktreesRoot`); commands, services, and values never cross this boundary. `POST /v1/configuration/repositories` carries `expectedRevision`, `expectedSourceDigest` (empty only when the file must not exist yet), an `operation` of `upsert` or `remove`, the `key`, and for upsert an `entry` with the same generic fields. The daemon compares both digests, edits the owner's YAML in place (preserving comments and untouched sections), recompiles the complete document, writes it atomically as an owner-only file, and stages a candidate that still requires `POST /v1/configuration/accept` with its exact digest. Nothing is written on `CONFIGURATION_REVISION_CONFLICT`, `CONFIGURATION_DESIRED_CHANGED`, `CONFIGURATION_ROOT_BOUND` (an existing key cannot change root), `CONFIGURATION_REPOSITORY_MISSING`, `CONFIGURATION_REPOSITORY_ENABLED` (removal requires a disabled entry in both the desired file and the accepted revision), `CONFIGURATION_REPOSITORY_REFERENCED` (a non-stopped environment still belongs to a repository being disabled or removed, or a managed worktree to one being removed), or `CONFIGURATION_INVALID`, whose message carries the compiler's bounded reason naming keys, fields, and lines but never a scalar value from the file. Symlinked, hard-linked, foreign-owned, or group-readable desired files are refused without modification.

Cleanup stays two-phase: `POST /v1/cleanup/plans` returns an inspectable plan with its revision, candidates, and protections, and `POST /v1/cleanup/plans/{planId}/apply` names the exact revision and candidate list. Apply is a claimed transaction: the daemon atomically records authorization for that exact request before touching any owned resource, journals each candidate outcome as it becomes final, and consumes the plan together with its result. `CleanupResult` additionally carries `claimedAt` and `attempts`. Conflicts are stable codes: `CLEANUP_PLAN_CHANGED` (unknown plan or revision), `CLEANUP_PLAN_EXPIRED`, `CLEANUP_PLAN_CONSUMED` (already applied with a different request), `CLEANUP_APPLY_IN_PROGRESS` (another request is applying it now; retryable), and `CLEANUP_APPLY_MISMATCH` (an incomplete apply claimed the same revision for a different candidate list). An apply cut short by cancellation answers `CLEANUP_INTERRUPTED`; repeating the identical request resumes it, replays candidates already final, and reports the interrupted candidate as `removed: false, reason: "interrupted"` when its removal cannot be proven complete. Repeating an identical completed request replays the same result.

Worktrees may publish `occupancy`: the held handoff leases of that worktree, at most 16. A lease is an explicit, conservative record that an owner-launched task was handed the worktree; Switchyard never infers one from a deep link, a process, or a transcript. `POST /v1/worktrees/{worktreeId}/occupancy` carries `{schemaVersion, requestId, worktreeId, holderKind, holderLabel}` and answers `200` with the `OccupancyLease` (`id`, `worktreeId`, `holderKind`, `holderLabel`, `state` of `held` or `released`, `acquiredAt`, optional `releasedAt`). `holderKind` is a bounded generic lowercase token such as `agent-task`; `holderLabel` is bounded single-line display text without path separators; neither names a host product, account, or person. Repeating a `requestId` returns the same lease. `POST /v1/worktrees/{worktreeId}/occupancy/{leaseId}/release` carries `{schemaVersion, requestId, worktreeId, leaseId}` and answers `200` with the released lease; releasing twice is idempotent. Both are synchronous local-state writes, not operations. Errors are `INVALID_OCCUPANCY_REQUEST`, `WORKTREE_NOT_FOUND`, `OCCUPANCY_LEASE_NOT_FOUND`, `OCCUPANCY_LIMIT`, `OCCUPANCY_REQUEST_CONFLICT`, and `OCCUPANCY_UNAVAILABLE`. A held lease ends only when a client releases it: the daemon never expires one, and `POST /v1/worktrees/{worktreeId}/archive` refuses an occupied worktree with `WORKTREE_OCCUPIED` both at acceptance and again immediately before the Git mutation.

Workspace mutations use the same asynchronous receipt pattern. Create establishes a new positively owned checkout; adopt promotes an eligible existing checkout from run-only inventory ownership to managed ownership without recreating it; archive removes only a positively owned checkout. Adoption is addressed as `POST /v1/worktrees/{worktreeId}/adopt` and carries no environment revision.

The daemon status endpoint remains the authoritative atomic machine-wide snapshot. Human CLI and MCP clients project it without changing authority: `sy status` selects the containing worktree, `sy status --all` returns inventory, `switchyard_context` requires an exact absolute workspace path, `switchyard_environment_status` requires an exact environment ID, and `switchyard_inventory` is deliberately global. An MCP server process working directory is never a worktree selector. No scoped result silently embeds the complete global snapshot.

Each repository may publish a runtime catalog with its ordered targets, default target, and known services. Service entries state whether Switchyard can launch them safely; unsupported services remain visible with a stable reason instead of disappearing or being treated as executable. A running or stopped environment records the target used for its most recent start.

Each repository may also publish an `observation` with `observedAt`, `lastAttemptAt`, `stale`, and a stable failure code. The daemon reconciles Git/worktree inventory periodically. A failed refresh preserves the last successful repository data and marks it stale instead of advancing only the enclosing snapshot timestamp.

Every newly accepted environment start captures the exact worktree HEAD, tracked-dirty bit, untracked-dirty bit, and observation time before the operation is persisted. Each projected service run carries that immutable source provenance. It describes what the run launched from; it is deliberately independent from the repository's later live observation.

Worktrees may include a `changes` projection. `committed` is the merge-base-to-HEAD numstat; `uncommitted` is HEAD-to-working-tree plus untracked text lines. `services` attributes direct service-root changes, while `sharedCommitted` and `sharedUncommitted` preserve repository-wide or shared-package changes without falsely assigning them to one service. Aggregate totals must exactly equal service plus shared attribution.

Worktrees may also include a `pullRequest` observation. `found` carries bounded pull-request metadata and the complete capped check list, `none` records a successful lookup without a matching pull request, and `unavailable` carries only a stable error code. A failed refresh preserves a prior `found` or `none` result with `stale: true`; GitHub availability never changes environment health. No GitHub credential, token source value, command output, or process environment is public contract state. Environment-scoped MCP context includes the matching PR number, URL, state, CI state, review state, local-head match, and stale bit.

## Audit events

`GET /v1/events` is the resumable audit history (10,000 most recent). The daemon appends each event inside the same transaction as the change it records, so history and durable state commit or roll back together, and every event carries the snapshot revision that was current when the change was made. Payloads carry only opaque identifiers, stable codes, and lifecycle vocabulary: never commands, environment values, paths, credentials, labels, or log contents.

| Kind | When | Payload |
| --- | --- | --- |
| `operation.created` | a mutation was persisted as an operation, before any side effect | `operationId`, `kind`, `state`, optional `runId`, `environmentId` |
| `operation.transitioned` | every operation state change, including `DAEMON_RESTARTED` failures applied at boot | the same fields plus `errorCode` on failure |
| `configuration.accepted` | the owner accepted one immutable configuration revision | `revision`, `digest` |
| `occupancy.acquired` | a handoff lease was recorded | `leaseId`, `worktreeId`, `holderKind` |
| `occupancy.released` | a handoff lease ended | `leaseId`, `worktreeId`, `holderKind` |
| `cleanup.applied` | a claimed cleanup apply completed and consumed its plan | `planId`, `planRevision`, `attempts`, `requested`, `removed`, `skipped`, `interrupted` |

Profile-action runs are operations, so their receipt is followed by the `operation.*` audit trail. Cleanup application is audited once per claim: `cleanup.applied` is appended in the transaction that records the completed claim and consumes the plan, so an interrupted or still-resumable apply has no completion event, an identical replay of a completed request appends nothing, and a completion whose event cannot be appended is rolled back rather than committed unaudited. The payload counts outcomes (`removed` + `skipped` + `interrupted` = `requested`) and never names candidate IDs, paths, sizes, or profiles; `skipped` covers `not-in-plan` and `changed-or-protected`.

## Exact-version declaration

Every request to a versioned route (`/v1/...`) carries `X-Switchyard-Schema-Version: 2`, the client's exact contract schema version. After authentication the daemon answers an undeclared, duplicated, unreadable, or different declaration with HTTP `426 Upgrade Required` and the stable error below; `/handshake` alone tolerates an undeclared request so any client can still learn which versions the daemon supports, but rejects a mismatched declaration the same way. A request body whose `schemaVersion` is a positive integer other than `2` is also answered with `426 UPGRADE_REQUIRED` rather than a generic validation error, even when the body carries fields this daemon does not know; a missing, zero, or non-integer `schemaVersion` remains an ordinary `400`. Exact-version bodies keep their full strict validation.

`fixtures/upgrade-required-error.json` is the envelope: `code` is `UPGRADE_REQUIRED`, `retryable` is `false`, `resourceKind` is `contract`, `currentState` is the daemon's exact version, `requestedState` is the client's readable declaration (empty when it was unreadable or absent), and `nextAction` is `upgrade_client` or `upgrade_daemon` depending on which side is older. Clients map the HTTP status alone to their upgrade-required state even when the envelope is unreadable, and treat a well-formed runtime descriptor or handshake from another contract generation the same way; only a malformed descriptor or handshake is a generic invalid-response error.

## Local transport

`fixtures/runtime.json` is atomically written to a mode-`0600` file only after the listener and state store are ready. Its endpoint must be ephemeral loopback HTTP. The bearer token is a separate mode-`0600` file containing a base64url random value; it never appears in the descriptor, URLs, process arguments, status, or logs.

All endpoints, including `/handshake`, require `Authorization: Bearer`. `fixtures/handshake.json` is the exact-version response shape. Responses use `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`; requests carrying a browser `Origin` are rejected. Operation diagnostics default to 8192 bytes from the tail of each available stream and accept an explicit per-stream limit from 256 through 32768 bytes.
