# Contract v2

Contract v2 is the local JSON boundary shared by the Go daemon, CLI, MCP clients, and Swift app. It is the Switchyard 0.2.0 contract and replaces contract v1, which shipped with 0.1.0.

## Changes from v1

Contract v2 changes only what the product contract actually changed when built-in adapters were replaced by private repository profiles:

- `schemaVersion` is the integer `2` on every status, request, receipt, descriptor, and handshake payload. The handshake advertises `supportedSchemaVersions: [2]` only; a v1 client receives the existing stable upgrade-required error and the app replaces its bundled daemon before reconnecting.
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

`fixtures/start-environment-request.json`, `fixtures/stop-environment-request.json`, and `fixtures/mutation-receipt.json` freeze the asynchronous environment-action boundary. Starting names a discovered worktree, a repository-configured target such as `testing`, and an explicit non-empty service set. Omitting the target at the CLI/MCP boundary selects the repository default. A target with `warnOnStart` requires `confirmedTargetId` to equal the resolved target for that start request; confirmation is never persisted as reusable authorization. Stopping names the environment in the route. Both carry a request ID, idempotency key, and optional expected environment revision; acceptance returns an operation ID immediately. Start receipts and their public operations also carry the same immutable `runId`, so clients never infer completion from a previously healthy run.

Profile actions are accepted private behavior, never client-side scripts. `GET /v1/actions` lists every action in the accepted configuration revision as `{id, repositoryId, profileKey, profileDigest, displayName, scope, risk, kind, lifecycle?, requiresConfirmation}`; it never exposes an executable, argument, or environment shape. `POST /v1/actions/run` carries a mutation request plus `repositoryId`, `actionId`, and exactly the target identifiers the action's scope requires (`worktreeId` for worktree scope, `environmentId` for environment scope, `environmentId` plus `serviceId` for service scope, none for machine or repository scope); a mismatch is `ACTION_SCOPE_MISMATCH`. `remote-write` actions require `confirmedActionId` to equal `actionId` on every run (`ACTION_CONFIRMATION_REQUIRED`); confirmation is never persisted. Lifecycle actions dispatch the existing prepare, start, and stop operations and return their receipts; `cleanup` answers `ACTION_REQUIRES_REVIEW` because cleanup is always plan then apply. Command actions become a `profile.action` operation pinned to the accepted revision: the idempotency fingerprint includes the profile and configuration digests, a drifted desired configuration fails closed with `CONFIGURATION_NOT_ACCEPTED`, environment-scoped runs serialize with other operations on that environment, and failures publish only a bounded `ACTION_COMMAND_FAILED`, `ACTION_TIMED_OUT`, `ACTION_COMMAND_INVALID`, `ACTION_COMMAND_START_FAILED`, or `ACTION_INTERRUPTED` error with exit status and an opaque `logReference` into Switchyard-owned bounded output files.

Workspace mutations use the same asynchronous receipt pattern. Create establishes a new positively owned checkout; adopt promotes an eligible existing checkout from run-only inventory ownership to managed ownership without recreating it; archive removes only a positively owned checkout. Adoption is addressed as `POST /v1/worktrees/{worktreeId}/adopt` and carries no environment revision.

The daemon status endpoint remains the authoritative atomic machine-wide snapshot. Human CLI and MCP clients project it without changing authority: `sy status` selects the containing worktree, `sy status --all` returns inventory, `switchyard_context` requires an exact absolute workspace path, `switchyard_environment_status` requires an exact environment ID, and `switchyard_inventory` is deliberately global. An MCP server process working directory is never a worktree selector. No scoped result silently embeds the complete global snapshot.

Each repository may publish a runtime catalog with its ordered targets, default target, and known services. Service entries state whether Switchyard can launch them safely; unsupported services remain visible with a stable reason instead of disappearing or being treated as executable. A running or stopped environment records the target used for its most recent start.

Each repository may also publish an `observation` with `observedAt`, `lastAttemptAt`, `stale`, and a stable failure code. The daemon reconciles Git/worktree inventory periodically. A failed refresh preserves the last successful repository data and marks it stale instead of advancing only the enclosing snapshot timestamp.

Every newly accepted environment start captures the exact worktree HEAD, tracked-dirty bit, untracked-dirty bit, and observation time before the operation is persisted. Each projected service run carries that immutable source provenance. It describes what the run launched from; it is deliberately independent from the repository's later live observation.

Worktrees may include a `changes` projection. `committed` is the merge-base-to-HEAD numstat; `uncommitted` is HEAD-to-working-tree plus untracked text lines. `services` attributes direct service-root changes, while `sharedCommitted` and `sharedUncommitted` preserve repository-wide or shared-package changes without falsely assigning them to one service. Aggregate totals must exactly equal service plus shared attribution.

Worktrees may also include a `pullRequest` observation. `found` carries bounded pull-request metadata and the complete capped check list, `none` records a successful lookup without a matching pull request, and `unavailable` carries only a stable error code. A failed refresh preserves a prior `found` or `none` result with `stale: true`; GitHub availability never changes environment health. No GitHub credential, token source value, command output, or process environment is public contract state. Environment-scoped MCP context includes the matching PR number, URL, state, CI state, review state, local-head match, and stale bit.

## Local transport

`fixtures/runtime.json` is atomically written to a mode-`0600` file only after the listener and state store are ready. Its endpoint must be ephemeral loopback HTTP. The bearer token is a separate mode-`0600` file containing a base64url random value; it never appears in the descriptor, URLs, process arguments, status, or logs.

All endpoints, including `/handshake`, require `Authorization: Bearer`. `fixtures/handshake.json` is the exact-version response shape. Responses use `Cache-Control: no-store` and `X-Content-Type-Options: nosniff`; requests carrying a browser `Origin` are rejected. Operation diagnostics default to 8192 bytes from the tail of each available stream and accept an explicit per-stream limit from 256 through 32768 bytes.
