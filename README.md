# Switchyard

A private, personal macOS control plane for Marketplace worktrees and their local runtime environments.

## North star

From any Marketplace worktree, one action should produce the smallest useful local environment without port collisions, cross-worktree state leaks, unexplained process forests, or terminal setup. The same environment must be understandable and controllable from a native macOS app, a human CLI, Codex, and Claude.

Switchyard has two first-class lifecycle layers:

- a **workspace** owns or adopts a Git worktree, resolves its repository toolchains, hydrates dependencies, and records a content-addressed readiness stamp;
- an **environment** builds selected workloads from a ready workspace and owns their ports, projections, infrastructure, processes, and health.

An environment binds together:

- a repository and worktree;
- its branch, base, pull request, and integration provenance;
- active agent sessions;
- requested and auto-detected services;
- stable port and infrastructure leases;
- generated routing configuration;
- owned native process groups and Docker resources;
- health, logs, alerts, resource use, and cleanup state.

## Product stance

- This is a private personal tool, not a Example or Example project.
- It is optimized fully for Theron's Marketplace workflow.
- Its core abstractions remain repository-neutral so another repository can be added through configuration or a small adapter rather than a rewrite.
- Marketplace is the first and initially only rich adapter.
- There is no customer, public product, generic plugin marketplace, or cross-platform requirement.
- Quality, safety, speed, and delight are not reduced merely because there is one user.

## Surfaces

- **SwiftUI app:** menu bar overview, command-center window, notifications, setup, repair, and all normal lifecycle actions.
- **Go daemon:** sole owner of runtime state, leases, processes, health, reconciliation, Docker resources, and SQLite persistence.
- **CLI:** current-worktree status by default, explicit global inventory, concise mutations, and stable JSON for each scope.
- **MCP:** explicit current-worktree context, single-environment status, and global inventory tools over the daemon. It never infers context or owns lifecycle state.

The app must ensure the daemon and agent connections are installed, running, current, and repairable. A user LaunchAgent owns daemon lifetime independently of the app. Normal use must not require a terminal command to start a background service, and quitting the app does not stop environments.

## Golden experience

1. Open Switchyard.
2. It discovers the main Marketplace checkout and all linked worktrees.
3. Create a managed worktree in Switchyard, explicitly adopt an eligible existing worktree into managed ownership, or select a discovered worktree for run-only use.
4. Start an environment. Switchyard first verifies the workspace fingerprint, installs the Marketplace Node version when absent, runs immutable dependency hydration when stale, then builds selected services, allocates stable free ports, materializes routing, starts isolated mutable infrastructure, launches owned process groups, and waits for health.
5. The app shows friendly URLs, logical services, aggregate CPU and memory, recent logs, Git/PR state, and any action needed.
6. A second worktree can run the same services concurrently without collisions or shared mutable queue/database state.
7. Codex or Claude passes its exact active workspace path and receives only that worktree's context; global inventory is a separate explicit action. Environment mutations still return compact state at the action boundary without background chat injection.
8. Stopping or cleaning an environment affects only resources Switchyard positively owns.

## Initial acceptance demonstration

Run `organizer` and `nonprofit-service` concurrently in two Marketplace worktrees. Both environments must:

- receive stable, distinct ports;
- route only to services in the same environment;
- use isolated mutable infrastructure;
- report health and logical process-group resource totals;
- survive app/daemon reconciliation;
- stop without touching the other environment or foreign processes;
- be controllable from the app, CLI, Codex MCP, and Claude MCP.

## Developer build

The baseline deliberately requires only the standard Go and Swift toolchains:

```bash
make check
make race
make ui-snapshots
make app-bundle
open dist/Switchyard.app
```

`make check` includes the real SwiftPM `SwitchyardTests` target plus the legacy cross-language contract verifier. `make ui-snapshots` renders deterministic wide, compact, menu-bar, light, and dark SwiftUI states offscreen into `app/.build/ui-snapshots`; it does not need to foreground or automate the running app. Keep bundle relaunches for the final lifecycle smoke test.

The packaged app contains both the SwiftUI executable and the Go daemon. On first launch it installs the daemon into the user's Application Support directory and registers a private user LaunchAgent; normal use does not require starting a daemon from a terminal. For development diagnostics after the app has launched:

```bash
"$HOME/Library/Application Support/Switchyard/bin/switchyard" doctor
"$HOME/Library/Application Support/Switchyard/bin/switchyard" status
"$HOME/Library/Application Support/Switchyard/bin/switchyard" status --all --json
```

`sy status` detects the containing known worktree, including when run from a subdirectory, and prints only its repository, workspace, Git/PR, environment, service, operation, and alert state. `sy status <worktree-id|branch|path>` selects another tree exactly. `sy status --all` is the deliberate machine-wide inventory; outside every known worktree, plain text status explains the miss and falls back to that inventory. Scoped `--json` emits the worktree context projection and refuses an unknown current directory rather than silently changing shape; `--all --json` preserves the complete daemon snapshot.

## Current milestone

The first end-to-end milestone is complete. The packaged app owns installation and repair, its LaunchAgent keeps the authenticated Go daemon alive, and the daemon discovers, safely creates, or explicitly adopts eligible Marketplace worktrees, persists workspace readiness and operations, hydrates dependencies, allocates isolated ports, prepares services, starts owned process groups and labelled ElasticMQ containers, initializes queues, observes live health/resources, and safely reconciles across app and daemon restarts. Create/adopt/archive and start/stop work through the SwiftUI app, CLI, and thin MCP surface. Human status is current-worktree-first; agents use an explicit absolute-path context tool, an exact environment poller, or deliberately global inventory. The app can also inspect and repair Codex and Claude MCP connections.

The two-worktree Marketplace acceptance run passed with `organizer` and `nonprofit-service` healthy at the same time on distinct ports and infrastructure. Stopping either environment affected only its positively owned processes, container, projection, and leases; pre-existing Marketplace services and the foreign `demo-elasticmq` container survived. See the [golden Marketplace acceptance record](docs/reviews/GOLDEN_MARKETPLACE_2026-08-14.md) and [next actions](docs/NEXT.md).

The Marketplace adapter now defines the full 18-service local runtime family. Serverless services receive owned per-service port overlays, selected SQS services share one worktree-scoped ElasticMQ broker with declarative queue initialization, and fixed local endpoints are remapped through narrowly scoped owned projections (including Slack's isolated DynamoDB Local and Donation Batch's SQS client). Catalog availability is published only from these validated executable definitions.

Repository inventory also publishes merge-base-to-HEAD and working-tree line counts. Direct service paths are attributed to their runtime service; shared packages remain visibly shared instead of being counted against an arbitrary service. The Swift app shows branch and working-tree deltas, opens exact existing worktrees in a new Zed window, and connects and fills both the menu-bar mark and live Dock icon when an environment is running; only runtime attention, process, and memory indicators may appear beside the menu-bar mark. Accepted environment mutations remain visibly transitional until their daemon operation is terminal; a dedicated rebuild-and-restart action performs a real stop followed by the normal preparation and start workflow. Branch ticket keys link to Jira and load a bounded summary, status, assignee, priority, and update time on demand through the authenticated read-only relay. A local per-worktree override can replace branch detection without changing repository files. Jira availability is not part of daemon startup or health.

Local Git/worktree inventory is reconciled every 30 seconds and carries its own observation freshness, so a new atomic snapshot cannot disguise stale repository data. Each accepted environment start records the exact source revision and dirty state it launched and returns the immutable run ID; app and agent clients verify that exact run before treating a rebuild as complete.

The daemon also observes pull requests and CI through the existing Keychain-backed GitHub CLI login. It publishes draft/ready/merged state, mergeability, review state, local-versus-remote head, and complete bounded check details to the app, scoped CLI/MCP context, global inventory, and environment mutation footer. Pending or recently changed work polls quickly and progressively backs off to a six-hour safety cadence as it becomes inactive. GitHub availability is explicitly independent from local environment readiness and health.

## Documents

- [Architecture](docs/ARCHITECTURE.md)
- [Marketplace adapter](docs/MARKETPLACE.md)
- [Safety invariants](docs/SAFETY.md)
- [Parallel build plan](docs/BUILD_PLAN.md)
- [Decision log](docs/DECISIONS.md)
- [Next actions](docs/NEXT.md)
- [Golden Marketplace acceptance](docs/reviews/GOLDEN_MARKETPLACE_2026-08-14.md)
- [Fable environment integration review](docs/reviews/FABLE_ENVIRONMENT_INTEGRATION_REVIEW.md)
