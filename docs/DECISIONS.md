# Decision log

Accepted decisions are durable until deliberately amended here. Open decisions are not permission to invent an answer that changes scope or external state.

## Accepted

### D-001: Personal private project

Switchyard lives in Theron's personal GitHub namespace as a private repository. It is unrelated to Example and Example except that it locally operates the Marketplace checkout the user already controls.

### D-002: Marketplace-first, repository-open core

Optimize deeply for Marketplace through a rich adapter. Keep the core vocabulary and control plane repository-neutral so future repositories require configuration or a small adapter rather than a rewrite. Do not build a general plugin marketplace before a second repository exists.

### D-003: Go control plane and Swift presentation

Go owns daemon, state, supervision, adapters, CLI, and MCP. SwiftUI owns the native macOS experience.

### D-004: Daemon owns state

The daemon is the single runtime-state writer. App, CLI, and MCP are clients. Per-agent MCP processes never own ports, processes, or cleanup state.

### D-005: App-first lifecycle

The app bundles, starts, upgrades, validates, and repairs the helper. Normal use requires no terminal daemon command. The app launches at login and provides Connection Doctor plus Repair All.

### D-006: Context rides with actions

Every MCP result includes a compact Forge-style environment context footer. Switchyard does not inject unsolicited chat messages, wake sleeping agents, or interrupt running agents.

### D-007: Personal local Marketplace manifest

`.switchyard.yaml` is schema-versioned but untracked. The app adds it to the shared local `.git/info/exclude`, never the public `.gitignore`.

### D-008: Existing Marketplace helper is untouched

Do not edit, wrap, replace, or require `scripts/start-changed.sh`. It remains available to colleagues. Switchyard implements its own adapter.

### D-009: Safety through positive ownership

Only positively owned process groups and labelled Docker resources may be stopped or removed. Cleanup is plan then apply. Dirty, unpushed, locked, or active worktrees are protected.

### D-010: Aggressive bounded parallelism

Use all useful Codex lanes plus external Fable work, with frozen contracts, separate worktrees, exclusive directory ownership, and explicit integration gates.

### D-011: Performance before disk minimalism

Use caches aggressively and report disk/reclaimability without nagging. Avoid unsafe sharing of mutable `node_modules`. A 1 TB drive means ordinary multi-gigabyte worktrees are not inherently alert-worthy.

### D-012: Fable is a first-class outsourced lane

Use the personal `claude-personal` profile with explicit `--model fable`. The alias was live-verified as canonical `claude-fable-5`.

### D-013: Switchyard is the final name

The private personal repository is `theronburger/switchyard` and the app identity is Switchyard.

### D-014: Launchd owns daemon lifetime

The app owns installation, approval, upgrade, and repair UX. A user LaunchAgent keeps the daemon alive independently of the app. CLI and MCP may ask launchd to kick an already-installed daemon but do not launch the GUI or implement runtime ownership.

### D-015: Authenticated loopback HTTP is the v1 transport

The daemon binds an ephemeral `127.0.0.1` port and atomically publishes a mode-`0600` endpoint descriptor. A separate mode-`0600` bearer token authenticates clients. Tokens never appear in URLs, logs, status, fixtures, or process arguments. The transport supports ordinary JSON and a resumable SSE event stream.

This was chosen over a Unix-domain socket because Swift `URLSession`, Go, the CLI, and both MCP clients can share one small implementation. The daemon rejects non-loopback binding, hostile browser origins, and stale or incompatible descriptors.

### D-016: Mutations are revisioned asynchronous operations

Mutations become persisted operations before side effects. They are serialized per environment, not globally, and return an operation ID immediately. Mutations support idempotency keys and expected-revision comparison. Snapshots are atomic and carry monotonic revisions.

Core IDs are opaque. Adapter names such as ticket or branch names are display fields. MCP footers are complete capped snapshots at a revision; they do not depend on hidden per-client diff state. Global tools return no implicit environment footer unless an environment is explicitly resolved.

### D-017: Initial integration scope

The app configures and validates both Codex and Claude MCP connections through inspectable read-modify-write plans. The first milestone discovers and adopts existing worktrees. Worktree create/remove and union materialization follow the runtime-control milestone.

### D-018: Mutable infrastructure is isolated

Mutable queues, databases, caches, volumes, and Compose projects are isolated per environment by default. V1 has no reference-counted shared mutable infrastructure. Immutable image and build caches may remain shared.

### D-019: Supervision survives daemon and app restarts

Managed children write directly to owned permission-restricted rotating log files; the daemon tails rather than owns irreplaceable pipes. Process groups persist PID, start time, group ID, and command fingerprints. Membership is revalidated immediately before signals to defend against PID and process-group reuse. The app quitting never stops environments.

## Open

### O-003: App identity and installation

Choose icon direction, `~/Applications` versus `/Applications`, Developer ID signing/notarization, and the final packaging flow. The bundle identifier is `com.theronburger.switchyard`.
