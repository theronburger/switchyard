# Fable task: Switchyard Wave 2 Swift lifecycle integration

You are implementing Switchyard Wave 2 in this repository.

Read `AGENTS.md` fully, then `README.md`, `docs/ARCHITECTURE.md`, `docs/SAFETY.md`, `docs/DECISIONS.md`, the contract v1 fixtures, and the current app source before editing.

Work only under `app/**`. Do not edit Go code, shared contracts, root manifests, Marketplace, or documentation.

The Go binary now has real modes:

- `switchyard daemon`
- `switchyard status [worktree-id|branch|path] [--all] [--json]` (current-worktree scope by default; explicit inventory with `--all`)
- `switchyard doctor [--json]`
- `switchyard mcp`

Its standard runtime files are `~/Library/Application Support/Switchyard/daemon/runtime.json` and `token`, both mode `0600`.

Deliver a production-minded Swift milestone that replaces fixture-only normal operation with a live daemon path while retaining explicit fixture preview/test modes.

Requirements:

1. The app itself ensures daemon installation, start, and repair. Normal use must require no terminal command. Use a user LaunchAgent owned and repairable from the UI, with safe atomic plist/install mechanics and clear macOS approval/error UX. Do not use `SMAppService` unless the current non-bundled SwiftPM packaging genuinely supports it, but design an adapter seam for future app-bundle packaging.
2. Before loading the bearer token or sending HTTP, validate runtime descriptor file security and prove PID plus actual Darwin process start time matches `processStartedAt`, so a stale descriptor with a reused port receives zero authenticated requests. Reject symlinks, nonregular files, oversized JSON/token files, noncanonical base64url 32-byte tokens, endpoint deviations, redirects, and version/schema/identity mismatches.
3. Make the command center and menu bar use live status and doctor by default, with bounded polling, visible disconnected/repairing states, and no chat injection.
4. Preserve fixture scenarios as explicit launch/debug support.
5. Add focused Swift checks or extend `SwitchyardContractCheck` for process identity, hostile descriptors, token validation, lifecycle transitions, and live-status model mapping.
6. Ensure the daemon binary location/install source is injectable for tests. Honestly represent any packaging limitation rather than pretending it works.

Run `swift build --package-path app` and the contract check if permissions allow. Do not commit.

At the end, report exact files changed, verification, remaining packaging blockers, and canonical model/cost metadata. Be adversarial about lifecycle and secret leakage.
