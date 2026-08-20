# Next actions

## Current state

- The public `theronburger/switchyard` repository, production CI, CodeQL, Homebrew Cask, and signed update path are live.
- The Go daemon persists revisioned state in SQLite and publishes an authenticated ephemeral `127.0.0.1` API.
- The packaged SwiftUI app installs, starts, checks, and repairs its own user LaunchAgent; fixture mode is explicit-only.
- The live daemon discovers profile-configured checkouts and linked worktrees without changing their tracked files.
- Persisted environment start/stop coordinates stable ports, private artifacts, labelled owned containers, initialization, exact accepted-profile commands, owned process groups, readiness, and rollback.
- Boot restores exact leases and environment state before serving, reconciles interrupted operations, and observes live service ownership, health, process counts, and aggregate memory.
- The CLI and MCP expose idempotent start/stop plus stable status/doctor contracts. The SwiftUI app exposes lifecycle actions and can inspect or repair Codex and Claude MCP registrations without holding daemon credentials.
- The two-worktree golden run passed end to end, including app quit, daemon restart, stop-one, and stop-all ownership proofs.
- Repository configuration now belongs under Application Support instead of in a checkout or shared Git metadata.
- Profile-configured service catalogs are startable. Shared services within one environment use the same environment-owned local infrastructure, while every worktree retains distinct ports and mutable resources.
- Worktree inventory reports committed and uncommitted line counts per direct service root, keeps shared-package changes explicit, opens exact worktrees in Zed, and drives configurable menu-bar indicators from the atomic daemon snapshot.
- Jira ticket keys and links are visible from branch names. Worktree detail loads bounded summary/status/assignee/priority/update metadata on demand through the app-owned `jira-claude-relay` client; failures stay local to that card and never affect daemon readiness.
- Repository/worktree Git state is reconciled every 30 seconds with explicit per-repository freshness. Every environment start persists its exact source revision and dirty state, and operation receipts identify the run that must appear before replacement is complete.

## Next build sequence

1. Add periodic container/disk inventory and surface dangling or unusually expensive resources as actionable, deduplicated alerts.
2. Add bounded per-service log tails, search, and reveal-in-Finder from the app without leaking log contents into status or MCP by default.
3. Extend GitHub/PR/CI provenance with union ancestry and richer failing-check diagnostics.
4. Finish the richer menu-bar and command-center presentation: notification policy, attention routing, environment history, resource trends, and one-click URLs.
5. Add affected-service inference and a polished **Run affected** action while retaining explicit service selection and the same durable plan contract.
6. Extend the current app-driven Codex and Claude connection conformance suite when either host changes its CLI contract.
7. Retry transient terminal publication before conservatively tearing down a verified-healthy environment, and derive cleanup budgets from the concrete execution plan.
8. Exercise concurrent profiles for a second repository while preserving the same generic control contracts.

## Completed milestone evidence

- [Fable environment integration review](reviews/FABLE_ENVIRONMENT_INTEGRATION_REVIEW.md)
- `make check` covers Go vet/tests, Swift build, Swift Testing suites, and 58 dependency-free Swift contract checks.
- `make race` passes across all Go packages.
- GitHub Actions is green for the accepted implementation commit.

## Known local prerequisites

- Go 1.26.5 is installed.
- Swift 6.3.3 is installed.
- Colima and the Docker CLI are installed.
- `claude-personal` is available in a zsh login shell.
- Fable was verified through the `claude-personal-fable` skill as `claude-fable-5`.
- Acceptance repositories and linked worktrees are under `~/Developer` on the development machine.

## Known local caveat

`xcode-select` currently points at a full Xcode installation whose license has not been accepted. Repository automation uses the installed Command Line Tools explicitly and does not accept the license or invoke `sudo` on the user's behalf.
