# Switchyard agent instructions

Read before implementation, in order:

1. `README.md`
2. `docs/DECISIONS.md`
3. `docs/ARCHITECTURE.md`
4. the relevant task document, usually `docs/CONFIGURATION.md`, `docs/SAFETY.md` or `docs/RELEASING.md`

The parent `../AGENTS.md` principles also apply when this checkout lives in the
development workspace.

## Product rules

- Keep Run, setup, readiness, logs, Stop and retry coherent for one exact workspace. Validate behavior with real commands; do not trust a UI label as proof.
- The Go helper executes commands and keeps runtime state. The app, CLI and MCP are clients.
- Keep repository-specific commands and values in private configuration outside consuming repositories. Product code, fixtures, documentation and bundled skills contain no consuming-repository identity or catalog.
- The app installs, starts and repairs its helper. Normal use must not require a manual daemon command.
- Signal only process groups launched for the current run. Never kill by executable name or port, adopt unknown processes, or perform global Docker cleanup.
- Prune previews and rechecks one exact Git worktree. Never force removal or remove primary, dirty, unpushed, locked, active or invalid worktrees.
- Do not edit consuming repositories' tracked files, public ignore rules or existing development helpers.
- Do not expose raw scripts, environment dumps, credentials, account identifiers or transcript contents in status or logs.
- Background work may inform the app; it must not inject messages, wake agents or interrupt their tasks.

## Code shape

- Swift owns native presentation and installation in `app/`.
- Go owns the executable in `cmd/switchyard/`, runtime and Git/configuration in `internal/workspaces/`, and HTTP in `internal/localapi/`.
- `internal/workspaces/types.go` and `contracts/v3/` define the shared JSON contract.
- Use ordinary commands, Git operations and small functions. Do not add another lifecycle framework, configuration compiler or parallel state model.
- Remove superseded code and claims when replacing behavior. Preserve unrelated user changes.
- If work is delegated, assign disjoint files first. Coordinate shared types, dependencies, manifests and fixtures explicitly.

## Verification

- Go: format, vet, focused tests, then `go test ./...`.
- Swift: focused tests, then the complete Swift check.
- UI claims require a freshly built app and visual verification.
- Runtime changes need real-process failure, cancellation and stop tests.
- Prune changes need preview and mutation assertions proving protected and foreign worktrees survive.
- Follow `docs/BUILD_PLAN.md` for acceptance checks and `docs/RELEASING.md` for packaging and release work.
