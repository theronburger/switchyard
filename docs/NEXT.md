# Next actions

## Current state

- The private `theronburger/switchyard` repository and CI are live.
- The Go daemon persists revisioned state in SQLite and publishes an authenticated ephemeral `127.0.0.1` API.
- The packaged SwiftUI app installs, starts, checks, and repairs its own user LaunchAgent; fixture mode is explicit-only.
- The live daemon discovers the Marketplace checkout and its linked worktrees without changing tracked Marketplace files.
- Native process groups, labelled Docker resources, stable ports, generated projections, health evaluation, compact MCP status, and paged event primitives have safety-focused implementations and race tests.
- The Marketplace `.switchyard.yaml` is local-only and excluded by the shared Git `info/exclude`; `scripts/start-changed.sh` remains untouched.

## Immediate integration sequence

1. Finish the persisted environment coordinator and bind Marketplace plans after stable ports are assigned.
2. Expose idempotent start/stop operations through the daemon contract.
3. Add the matching CLI, MCP, and Swift app actions without moving lifecycle ownership out of the daemon.
4. Reconcile operation, process, container, projection, health, and event state after daemon restart.
5. Run organizer and nonprofit-service in two worktrees with colliding preferred ports, then prove isolated routing and ownership-safe stop.
6. Add app-managed Codex and Claude MCP connection inspection/repair.

## Known local prerequisites

- Go 1.26.5 is installed.
- Swift 6.3.3 is installed.
- Colima and the Docker CLI are installed.
- `claude-personal` is available in a zsh login shell.
- Fable was verified through the `claude-personal-fable` skill as `claude-fable-5`.
- Marketplace and its linked worktrees are under `/Users/example/Developer`.

## Known local caveat

`xcode-select` currently points at a full Xcode installation whose license has not been accepted. Repository automation uses the installed Command Line Tools explicitly and does not accept the license or invoke `sudo` on the user's behalf.
