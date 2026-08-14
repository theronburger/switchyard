# Next actions

## Confirmed launch choices

- final name: Switchyard;
- private repository: `theronburger/switchyard`;
- both Codex and Claude MCP connections are first-class;
- first milestone adopts existing worktrees; create/remove follows;
- no Marketplace tracked files or agent configurations are changed during the spine build.

## Immediate build sequence

1. Initialize the private repository and create its private personal remote.
2. Scaffold Go, Swift, contract fixtures, and CI/check commands.
3. Freeze Wave 0 directory ownership.
4. Spawn the Wave 1 Codex lanes and launch the Fable Swift lane.

## Known local prerequisites

- Go 1.26.5 is installed.
- Swift 6.3.3 is installed.
- Colima and the Docker CLI are installed.
- `claude-personal` is available in a zsh login shell.
- Fable was verified through the `claude-personal-fable` skill as `claude-fable-5`.
- Marketplace and its linked worktrees are under `/Users/example/Developer`.

## First technical spikes

These can run in parallel after scaffolding:

- Go daemon plus SQLite state and a fixture status endpoint.
- Swift app decoding and rendering the fixture status.
- Unix socket versus loopback transport comparison.
- Marketplace read-only worktree and affected-service inventory.
- owned process-group start/stop/reconcile harness using harmless fake services.
- MCP tool returning the capped environment context footer.
