# Build and verification

The product paths are `app/`, `cmd/switchyard/`, `internal/workspaces/`,
`internal/localapi/` and `contracts/v3/`. Keep changes focused on the behavior the
user needs. Shared JSON types and fixtures must agree before clients consume a
contract change.

## Local checks

```bash
make check
make race
make app-bundle
```

`make check` covers Go formatting, vet and tests, Swift checks and the shared
contract. Run focused tests while changing code, then the complete relevant
check. UI changes also require a freshly built app and visual inspection.

## Runtime acceptance

Use disposable Git repositories and real commands to verify:

- two workspaces start from one Run each, including required setup, on different ports;
- a foreign listener survives when it occupies a preferred port;
- setup failure, service exit and readiness failure identify the exact run and useful logs;
- cancellation during setup or startup stops that run and permits retry;
- stopping one workspace leaves the other reachable;
- command descendants stop when the helper exits;
- closing and reopening the app preserves running state and workspace choices;
- restarting the helper stops prior runs while retaining configuration and choices;
- the CLI and MCP operate on the same exact workspace shown in the app.

For Git changes, prove primary, dirty, unpushed, locked, active and invalid
worktrees cannot be removed. Test identical branch names and directory basenames,
changes after prune preview, missing registrations and unrelated worktrees.

At least one real configured repository also needs setup and start verification.
Synthetic success does not prove its private recipe includes required tools,
ignored files or runtime settings. Keep that private configuration and evidence
out of public fixtures and documentation.

## Release checks

```bash
make release-checks
make release-dry-run
```

Follow [RELEASING.md](RELEASING.md) for packaging, signatures, Sparkle updates,
Homebrew and publication. Do not treat a passing source test as proof that the
installed app contains the correct helper or that its update path works.
