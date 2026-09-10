<div align="center">
  <img src="packaging/SwitchyardIcon-Source.png" alt="Switchyard icon" width="200">
  <h1>Switchyard</h1>
  <p><strong>Run multiple Git worktrees side by side on your Mac.</strong></p>
  <p>
    <a href="https://github.com/theronburger/switchyard/actions/workflows/ci.yml"><img src="https://github.com/theronburger/switchyard/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
    <a href="https://github.com/theronburger/switchyard/releases/latest"><img src="https://img.shields.io/github/v/release/theronburger/switchyard?label=release" alt="Latest release"></a>
  </p>
</div>

Choose a workspace, target and services, then click **Run**. Switchyard executes
the configured setup commands, starts services on separate local ports, and
checks readiness. Each workspace has its own choices, progress, logs and Stop
action. The app, CLI, Codex and Claude Code use the same local helper.

## Install

Requires macOS 15 or newer, Git, and the development tools your app uses.
The app supports Apple Silicon and Intel Macs.

Install the app and `sy` CLI, trust its Homebrew Cask, and open it:

```bash
brew tap theronburger/tap && brew trust --cask theronburger/tap/switchyard && brew install --cask switchyard && xattr -dr com.apple.quarantine "/Applications/Switchyard.app" && open -a "Switchyard"
```

Releases use the self-signed `Theron Burger Apps Release` identity and are not
notarized. The quarantine acknowledgement above applies only to the installed
app. Sparkle verifies in-app updates with a separate Ed25519 signing key.
Archives are also available from [GitHub Releases](https://github.com/theronburger/switchyard/releases/latest).

The app installs and starts its helper. Add a repository and save its setup,
services and targets in private configuration. See the
[configuration guide](docs/CONFIGURATION.md) for a two-service example and the
files a fresh worktree needs.

## Use

Switchyard discovers Git's registered worktrees. Run includes setup; Stop also
cancels setup or startup. A failed run shows its current step and logs. Fix the
cause and Run again. Closing the app leaves services running. Restarting the
helper stops its runs; saved configuration and workspace choices remain.

```bash
sy status
sy run . --wait
sy logs .
sy stop .
sy prepare . --wait
sy open .
```

These commands resolve the current worktree, including from a child directory.
Use `sy status --all` for all configured repositories. `sy open .` opens that
exact workspace in the native app.

Create a worktree in the app or with `sy create REPOSITORY_ID BRANCH`.
`sy prune-plan PATH` previews deletion. Prune checks the exact worktree again and
refuses primary, locked, dirty, unpushed, active or invalid worktrees before
asking Git to remove it without force.

## Agents and configuration

Use **Connect / repair** in the app to register the bundled MCP helper with
Codex or Claude Code. Agents can inspect an exact workspace, run setup and
services, read logs, stop, configure, and manage worktrees through the same API.
Codex owns its tasks; Switchyard can open an existing task associated with the
exact workspace directory. It does not inject messages or wake agents.

Repository commands and environment settings live in
`~/Library/Application Support/Switchyard/config.json`. Saving applies the
configuration for future runs. Switchyard adds no configuration to consuming
repositories and does not alter their tracked files or existing development
helpers. Setup commands must explicitly supply any required ignored local files.

## Updates and uninstall

Use **Check for Updates…** in the app menu for signed Sparkle updates. The app
also handles installation and repair of its bundled helper. Release signing,
Homebrew publishing and packaging checks are documented in the
[release runbook](docs/RELEASING.md).

Remove agent registrations before uninstalling:

```bash
codex mcp remove switchyard
claude mcp remove switchyard --scope user
brew uninstall --cask switchyard
```

Homebrew stops the app and helper. Add `--zap` to remove app-managed runtime and
preferences. If a managed `switchyard` skill was installed, remove it separately
when it is no longer wanted.

## Development

The native app is in `app/`, the executable in `cmd/switchyard/`, command
execution and Git/configuration code in `internal/workspaces/`, and HTTP clients
and server in `internal/localapi/`. The app and helper share
[contract v3](contracts/v3/README.md).

```bash
make check
make race
make app-bundle
make release-checks
make release-dry-run
```

Read the [architecture](docs/ARCHITECTURE.md), [safety rules](docs/SAFETY.md), and
[verification guide](docs/BUILD_PLAN.md). Security reporting and release
verification are covered in [SECURITY.md](SECURITY.md).

Switchyard is a personal project published for transparency. No license is
granted unless one is added explicitly.
