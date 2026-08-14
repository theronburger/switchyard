# Switchyard

A private, personal macOS control plane for Marketplace worktrees and their local runtime environments.

## North star

From any Marketplace worktree, one action should produce the smallest useful local environment without port collisions, cross-worktree state leaks, unexplained process forests, or terminal setup. The same environment must be understandable and controllable from a native macOS app, a human CLI, Codex, and Claude.

The first-class object is an **environment**, not a worktree. An environment binds together:

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
- **CLI:** concise human commands and stable JSON output.
- **MCP:** thin agent-facing projection over the daemon. It never owns lifecycle state.

The app must ensure the daemon and agent connections are installed, running, current, and repairable. A user LaunchAgent owns daemon lifetime independently of the app. Normal use must not require a terminal command to start a background service, and quitting the app does not stop environments.

## Golden experience

1. Open Switchyard.
2. It discovers the main Marketplace checkout and all linked worktrees.
3. Choose a worktree and press **Run affected**.
4. Switchyard detects the changed Marketplace services, allocates stable free ports, materializes the routing environment, starts isolated mutable infrastructure, launches owned process groups, and waits for health.
5. The app shows friendly URLs, logical services, aggregate CPU and memory, recent logs, Git/PR state, and any action needed.
6. A second worktree can run the same services concurrently without collisions or shared mutable queue/database state.
7. Codex or Claude sees the same compact environment context whenever it calls a Switchyard tool, without background chat injection.
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
make app-bundle
open dist/Switchyard.app
```

The packaged app contains both the SwiftUI executable and the Go daemon. On first launch it installs the daemon into the user's Application Support directory and registers a private user LaunchAgent; normal use does not require starting a daemon from a terminal. For development diagnostics after the app has launched:

```bash
"$HOME/Library/Application Support/Switchyard/bin/switchyard" doctor
"$HOME/Library/Application Support/Switchyard/bin/switchyard" status --json
```

## Current milestone

The first end-to-end milestone is complete. The packaged app owns installation and repair, its LaunchAgent keeps the authenticated Go daemon alive, and the daemon discovers Marketplace worktrees, persists operations, allocates isolated ports, prepares services, starts owned process groups and labelled ElasticMQ containers, initializes queues, observes live health/resources, and safely reconciles across app and daemon restarts. Start and stop work through the SwiftUI app, CLI, and thin MCP surface; the app can also inspect and repair Codex and Claude MCP connections.

The two-worktree Marketplace acceptance run passed with `organizer` and `nonprofit-service` healthy at the same time on distinct ports and infrastructure. Stopping either environment affected only its positively owned processes, container, projection, and leases; pre-existing Marketplace services and the foreign `demo-elasticmq` container survived. See the [golden Marketplace acceptance record](docs/reviews/GOLDEN_MARKETPLACE_2026-08-14.md) and [next actions](docs/NEXT.md).

## Documents

- [Architecture](docs/ARCHITECTURE.md)
- [Marketplace adapter](docs/MARKETPLACE.md)
- [Safety invariants](docs/SAFETY.md)
- [Parallel build plan](docs/BUILD_PLAN.md)
- [Decision log](docs/DECISIONS.md)
- [Next actions](docs/NEXT.md)
- [Golden Marketplace acceptance](docs/reviews/GOLDEN_MARKETPLACE_2026-08-14.md)
- [Fable environment integration review](docs/reviews/FABLE_ENVIRONMENT_INTEGRATION_REVIEW.md)
