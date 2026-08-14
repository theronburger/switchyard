# Switchyard agent instructions

Read these files before implementation, in order:

1. `README.md`
2. `docs/DECISIONS.md`
3. `docs/ARCHITECTURE.md`
4. the task-specific document under `docs/`

The parent `/Users/example/Developer/AGENTS.md` principles also apply.

## Invariants

- The daemon is the only runtime-state writer. The app, CLI, and MCP are clients.
- MCP contains no lifecycle or repository logic.
- Repository-specific behavior belongs behind an adapter boundary. Marketplace is the first adapter, not logic smeared through the core.
- The app owns setup and repair. Do not require the user to start the daemon manually.
- Never kill processes by executable name. Act only on positively owned process groups.
- Never perform a global Docker prune. Act only on labelled owned resources after an inspectable plan.
- Never remove a dirty, unpushed, locked, or active worktree.
- Do not edit Marketplace's tracked files, public `.gitignore`, or `scripts/start-changed.sh`.
- Do not log complete process command lines, environment variables, credentials, account identifiers, or transcript contents.
- A background monitor informs the app. It does not inject chat messages, wake sleeping agents, or interrupt running agents.

## Code shape

- Go owns the daemon, core domain, storage, supervisor, adapters, CLI, and MCP.
- Swift owns the native app and presentation.
- The cross-language boundary is a small versioned local JSON contract with fixtures.
- Keep the core generic but concrete. Do not build a dynamic plugin framework before a second repository exists.
- Prefer small packages with one owner and one reason to change.
- Process, lease, health, and cleanup state transitions must be explicit and testable.

## Parallel work

Follow `docs/BUILD_PLAN.md`. Each lane owns disjoint directories. Shared contract, dependency, manifest, and generated files are coordinator-owned unless reassigned explicitly.

Fable runs through the installed `claude-personal-fable` skill or:

```bash
zsh -lic 'claude-personal --model fable --print "PROMPT"'
```

Never silently fall back from Fable. The verified canonical model is `claude-fable-5`.

## Verification

- Go changes: format, vet, focused tests, then `go test ./...`.
- Swift changes: focused tests, then the repository's complete Swift check.
- UI claims require a freshly built app and visual verification.
- Lifecycle changes require failure-path tests, not only happy paths.
- Cleanup changes require a dry-run/plan assertion proving foreign resources survive.
