# Decision log

Accepted decisions are durable until deliberately amended here. Open decisions are not permission to invent an answer that changes scope or external state.

## Accepted

### D-001: Personal public project

Switchyard lives publicly in Theron's personal GitHub namespace. It is a personal project that locally operates repositories the user already controls.

### D-002: Generic product, private repository profiles

Keep repository identity, commands, service catalogs, and values out of the product. Store schema-versioned profiles under Application Support so several repositories can be configured concurrently without writing configuration into their worktrees. Improve real repository performance by adding reusable, declarative primitives rather than repository branches in application code.

### D-003: Go control plane and Swift presentation

Go owns daemon, state, supervision, adapters, CLI, and MCP. SwiftUI owns the native macOS experience.

### D-004: Daemon owns state

The daemon is the single runtime-state writer. App, CLI, and MCP are clients. Per-agent MCP processes never own ports, processes, or cleanup state.

### D-005: App-first lifecycle

The app bundles, starts, upgrades, validates, and repairs the helper. Normal use requires no terminal daemon command. The app launches at login and provides Connection Doctor plus Repair All.

### D-006: Context is explicit and rides with actions

For current-task reads, MCP requires the exact absolute workspace path and returns a worktree-scoped projection. A separate tool reads one exact environment and a deliberately global tool reads inventory; MCP process working directory and inventory ordering never imply context. Accepted environment mutations include a compact Forge-style context footer. Switchyard does not inject unsolicited chat messages, wake sleeping agents, or interrupt running agents.

### D-007: Private external configuration

Configuration is schema-versioned and stored under Application Support, never in the consuming repository. Runtime artifacts are generated in Switchyard-owned private storage and projected only when an accepted profile explicitly requires them.

### D-008: Existing repository tooling is untouched

Do not edit, wrap, or replace repository tooling. Switchyard invokes only the exact commands in an accepted private profile.

### D-009: Safety through positive ownership

Only positively owned process groups and labelled Docker resources may be stopped or removed. Cleanup is plan then apply. Dirty, unpushed, locked, or active worktrees are protected.

### D-009a: Workspace precedes environment

Repository preparation is a repository-neutral durable workspace lifecycle. Existing worktrees are auto-discovered as run-only `adopted`; Switchyard-created worktrees are `managed`. Explicit adoption promotes only a clean, pushed, exact-repository checkout inside the configured managed root into `managed` ownership. Every environment start ensures the workspace fingerprint and readiness before service builds. Only managed worktrees with live identity proof may be archived, never with force.

### D-010: Aggressive bounded parallelism

Use all useful Codex lanes plus external Fable work, with frozen contracts, separate worktrees, exclusive directory ownership, and explicit integration gates.

### D-011: Performance before disk minimalism

Use caches aggressively and report disk/reclaimability without nagging. Avoid unsafe sharing of mutable `node_modules`. A 1 TB drive means ordinary multi-gigabyte worktrees are not inherently alert-worthy.

### D-012: Fable is a first-class outsourced lane

Use the personal `claude-personal` profile with explicit `--model fable`. The alias was live-verified as canonical `claude-fable-5`.

### D-013: Switchyard is the final name

The public personal repository is `theronburger/switchyard` and the app identity is Switchyard.

### D-014: Launchd owns daemon lifetime

The app owns installation, approval, upgrade, and repair UX. A user LaunchAgent keeps the daemon alive independently of the app and declares the main app bundle through `AssociatedBundleIdentifiers` so macOS can attribute the Background Item. A generated-plist change reloads only Switchyard's job with bootout/bootstrap; a helper-only update uses the scoped kickstart path. CLI and MCP may ask launchd to kick an already-installed daemon but do not launch the GUI or implement runtime ownership.

### D-015: Authenticated loopback HTTP is the v1 transport

The daemon binds an ephemeral `127.0.0.1` port and atomically publishes a mode-`0600` endpoint descriptor. A separate mode-`0600` bearer token authenticates clients. Tokens never appear in URLs, logs, status, fixtures, or process arguments. The transport supports ordinary JSON and a resumable SSE event stream.

This was chosen over a Unix-domain socket because Swift `URLSession`, Go, the CLI, and both MCP clients can share one small implementation. The daemon rejects non-loopback binding, hostile browser origins, and stale or incompatible descriptors.

### D-016: Mutations are revisioned asynchronous operations

Mutations become persisted operations before side effects. They are serialized per environment, not globally, and return an operation ID immediately. Mutations support idempotency keys and expected-revision comparison. Snapshots are atomic and carry monotonic revisions.

Core IDs are opaque. Adapter names such as ticket or branch names are display fields. MCP mutation footers are complete capped snapshots at a revision; they do not depend on hidden per-client diff state. Global inventory has no implicit current worktree or environment, while dedicated context reads require an explicit path or ID.

### D-017: Initial integration scope

The app configures and validates both Codex and Claude MCP connections through each host's exact CLI, with owner-controlled inspection and guarded rollback around failed mutations. The first milestone discovers and adopts existing worktrees. Worktree create/remove and union materialization follow the runtime-control milestone.

### D-018: Mutable infrastructure is isolated

Mutable queues, databases, caches, volumes, and Compose projects are isolated per environment by default. V1 has no reference-counted shared mutable infrastructure. Immutable image and build caches may remain shared.

### D-019: Supervision survives daemon and app restarts

Managed children write directly to owned permission-restricted rotating log files; the daemon tails rather than owns irreplaceable pipes. Process groups persist PID, start time, group ID, and command fingerprints. Membership is revalidated immediately before signals to defend against PID and process-group reuse. The app quitting never stops environments.

### D-020: One Switchyard mark with stateful presentation

The dock and menu-bar identities use one switch-track geometry. The Dock icon composites that exact geometry over a single alpha-cropped graphite tile with a raised bezel, fixed padding, and deterministic centering; both bundled and live variants reuse the same substrate. The menu-bar mark remains a monochrome template. While the app is running, both the live Dock icon and menu-bar mark render four disconnected rings while idle, then connect the diagonal route and fill its endpoints while an environment is running. Optional text indicators are user-configurable.

### D-021: GitHub CLI owns GitHub authentication

Switchyard reads pull-request and CI status through the user's existing authenticated `gh` session. It does not request, store, display, or forward a personal access token or app key. The daemon executes a resolved absolute `gh` binary with exact arguments, no shell, bounded output, a noninteractive sanitized environment, and no token-valued environment variables. GitHub observation is asynchronous and cannot block daemon readiness or alter environment health. Last-known PR state survives transient failures and polling backs off as branch activity ages.

### D-022: Freshness and run provenance are explicit

An atomic snapshot timestamp does not imply that every external subsection was freshly observed. Repository inventory therefore publishes its own successful observation time, latest attempt time, stale bit, and stable failure code. The daemon reconciles Git/worktree state periodically and preserves last-known data as explicitly stale when observation fails.

Every accepted environment start captures the exact Git revision and working-tree dirty state before persistence. The operation, receipt, and resulting service runs share one immutable run ID; service runs also carry that source snapshot. Clients declare a start complete only when the exact operation succeeds and the environment publishes the accepted run ID.

### D-023: Self-signed universal distribution

The first public release uses a universal Homebrew Cask and signed Sparkle updates. The stable self-signed publisher identity is `Theron Burger Apps Release`; Switchyard has a distinct Ed25519 Sparkle key. Because no Apple Developer identity exists, the frontend alone carries the library-validation exception needed to load Sparkle, and the explicit installation flow removes quarantine only from `/Applications/Switchyard.app` after Homebrew installation. Release assets include checksums, a CycloneDX SBOM, and GitHub provenance attestations.

### D-024: Private repository profiles supersede built-in adapters

Repository behavior is accepted private configuration stored only under Switchyard's Application Support directory. One daemon manages multiple configured repositories concurrently. Product code, tests, fixtures, documentation, and bundled skills contain no consuming-repository identity or catalog. Repository-local manifests, projections, and ignore-file edits are removed.

### D-025: Configuration acceptance authorizes compiled behavior

The owner reviews and accepts one immutable configuration revision as a transaction. Its preview includes every resolved executable, argument shape, working directory, non-secret environment shape, private artifact digest, shell, interpreter, and generated wrapper. Routine execution of that accepted revision does not prompt again. A changed revision or executable fingerprint requires re-acceptance; explicitly high-risk targets and actions may still require per-run confirmation. MCP and unattended setup hooks can execute accepted behavior but cannot accept configuration.

### D-026: Contract v2 names profiles, pins digests, and bounds state

Switchyard 0.2.0 publishes contract v2. The public repository record carries `profileKey` instead of the legacy `adapter` field; no client-facing payload names an adapter. Contract v2 changed only where the product contract changed: every other v1 shape is carried forward and the handshake advertises only version 2.

Pinned operation intent identifies the accepted repository-profile digest, not an adapter, and every environment result records that digest. A restarted daemon recovers the exact pinned payload from retained accepted configuration revisions even after later acceptances; a pinned digest without its payload fails boot closed.

The 0.2.0 release is a clean cutover. Switchyard is single-user, so 0.1.x app state is deleted or archived and 0.2.0 installs fresh; the product carries no compatibility machinery for 0.1.x state, operations, or runtime ownership, and the former in-place state migration was retired.

Durable state is bounded transactionally: one current snapshot row, 10,000 events, 500 terminal operations, 16 staged candidates, and 16 unreferenced configuration revisions, with every referenced or incomplete record retained regardless of age. Only live environment resources pin a revision; stopped results are history.

### D-027: Clients declare exact versions; occupancy is explicit

Every versioned daemon request declares the client's exact contract schema version in `X-Switchyard-Schema-Version`. A mismatch, an undeclared versioned request, or a request body from another contract generation is answered with HTTP 426 and the stable `UPGRADE_REQUIRED` error naming the daemon's version and which side is older; strict validation of exact-version bodies is unchanged. Go and Swift clients map that status, a mismatched handshake, and a well-formed descriptor from another generation to their upgrade-required state rather than to generic invalid or incompatible errors.

Worktree occupancy is never inferred. The app records one explicit, conservative handoff lease, with a generic holder kind and bounded label, before it launches a task into a worktree: a refused lease prevents the launch, a failed launch releases the lease, and a lease that cannot be released after a failed launch is kept as protection and reported to the owner. Only an owner release ends a lease. Held leases are published on the worktree, survive daemon restarts and inventory rebuilds, and block archive (`WORKTREE_OCCUPIED`) at acceptance and again immediately before the Git mutation. Occupancy, operation lifecycle, configuration acceptance, and cleanup apply completion append audit events inside the transaction that performs the change. A background monitor may inform the app through the snapshot and event feed; it still never injects chat messages, wakes sleeping agents, or interrupts running agents.

### D-028: Agent hosts own task and worktree creation

**Decision:** Switchyard no longer launches agent tasks or records app-created handoff leases. Codex creates each task and task worktree inside the owner's existing project. Its setup hook calls `sy prepare . --wait`, allowing Switchyard to discover and prepare that external worktree through the accepted generic profile. Switchyard finds existing Codex task IDs with a bounded, read-only app-server `thread/list` query filtered by the exact worktree `cwd`, and opens the selected task using `codex://threads/{id}`. `sy open .` provides the reverse navigation through the packaged `switchyard://worktrees/{id}` route.

**Why:** A Switchyard-side “start task” action necessarily began with a worktree that already existed, while Codex's native task model creates one worktree per task. Treating Switchyard as the creator produced a second project container and the wrong lifecycle. Exact cross-navigation preserves each product's ownership boundary and requires no consuming-repository identity in Switchyard.

**Supersedes:** D-027's app-launch and app-recorded-lease behavior. The generic daemon occupancy contract remains versioned independently; the native app does not use it for Codex discovery.

## Open

There are no open release-blocking identity or installation decisions. The bundle identifier is `com.theronburger.switchyard`.
