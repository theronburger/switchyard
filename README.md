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

The app-owned daemon lifecycle, authenticated local API, SQLite state, live Marketplace/worktree inventory, safe native-process host, labelled Docker host, port allocator, health evaluation, CLI/MCP status surfaces, resumable event pages, and native app packaging are implemented. Starting real Marketplace environments through the public action surfaces and the two-worktree golden demonstration remain in progress; see [Next actions](docs/NEXT.md).

## Documents

- [Architecture](docs/ARCHITECTURE.md)
- [Marketplace adapter](docs/MARKETPLACE.md)
- [Safety invariants](docs/SAFETY.md)
- [Parallel build plan](docs/BUILD_PLAN.md)
- [Decision log](docs/DECISIONS.md)
- [Next actions](docs/NEXT.md)
