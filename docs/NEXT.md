# Next actions

## Current state

- The private `theronburger/switchyard` repository and CI are live.
- The Go daemon persists revisioned state in SQLite and publishes an authenticated ephemeral `127.0.0.1` API.
- The packaged SwiftUI app installs, starts, checks, and repairs its own user LaunchAgent; fixture mode is explicit-only.
- The live daemon discovers the Marketplace checkout and its linked worktrees without changing tracked Marketplace files.
- Persisted environment start/stop coordinates stable ports, local-only projections, labelled ElasticMQ containers, queue initialization, exact Marketplace preparation commands, owned process groups, readiness, and rollback.
- Boot restores exact leases and environment state before serving, reconciles interrupted operations, and observes live service ownership, health, process counts, and aggregate memory.
- The CLI and MCP expose idempotent start/stop plus stable status/doctor contracts. The SwiftUI app exposes lifecycle actions and can inspect or repair Codex and Claude MCP registrations without holding daemon credentials.
- The two-worktree Marketplace golden run passed end to end, including app quit, daemon restart, stop-one, and stop-all ownership proofs.
- The Marketplace `.switchyard.yaml` is local-only and excluded by the shared Git `info/exclude`; `scripts/start-changed.sh` remains untouched.

## Next build sequence

1. Add periodic repository/worktree/container/disk inventory and surface dangling or unusually expensive resources as actionable, deduplicated alerts.
2. Add bounded per-service log tails, search, and reveal-in-Finder from the app without leaking log contents into status or MCP by default.
3. Add Marketplace GitHub/PR/CI provenance so an environment explains its branch, review state, union ancestry, and failing checks.
4. Finish the richer menu-bar and command-center presentation: notification policy, attention routing, environment history, resource trends, and one-click URLs.
5. Add affected-service inference and a polished **Run affected** action while retaining explicit service selection and the same durable plan contract.
6. Exercise app-driven Codex and Claude repair on the real profiles, then add a small connection conformance check to CI fixtures.
7. Retry transient terminal publication before conservatively tearing down a verified-healthy environment, and derive cleanup budgets from the concrete execution plan.
8. Add another repository only when a real second-repository workflow appears; preserve the generic control contracts and keep Marketplace deliberately first-class.

## Completed milestone evidence

- [Golden Marketplace acceptance, 2026-08-14](reviews/GOLDEN_MARKETPLACE_2026-08-14.md)
- [Fable environment integration review](reviews/FABLE_ENVIRONMENT_INTEGRATION_REVIEW.md)
- `make check` covers Go vet/tests, Swift build, and 51 dependency-free Swift contract checks.
- `make race` passes across all Go packages.
- GitHub Actions is green for the accepted implementation commit.

## Known local prerequisites

- Go 1.26.5 is installed.
- Swift 6.3.3 is installed.
- Colima and the Docker CLI are installed.
- `claude-personal` is available in a zsh login shell.
- Fable was verified through the `claude-personal-fable` skill as `claude-fable-5`.
- Marketplace and its linked worktrees are under `/Users/example/Developer`.

## Known local caveat

`xcode-select` currently points at a full Xcode installation whose license has not been accepted. Repository automation uses the installed Command Line Tools explicitly and does not accept the license or invoke `sudo` on the user's behalf.
