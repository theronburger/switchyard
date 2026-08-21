# Changelog

All notable changes are documented here. The project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Release Please generates each version section from Conventional Commits when it opens the release pull request. Do not add an Unreleased section; notes for non-conventional commits are added by editing the release pull request body before merge.

## [0.2.0](https://github.com/theronburger/switchyard/compare/v0.1.0...v0.2.0) (2026-08-21)


### ⚠ BREAKING CHANGES

* repository runtime behavior is accepted only through private profiles, and release/install paths reject legacy overrides.

### Added

* migrate Switchyard to private repository profiles ([cb13968](https://github.com/theronburger/switchyard/commit/cb13968577dca57bd0ffd6fd5fefa999afa9a19d))

### Fixed

* stabilize ownership verification for short-lived processes and transitioning descendant groups


### Maintenance

* **deps:** bump the go-dependencies group with 2 updates ([37ce722](https://github.com/theronburger/switchyard/commit/37ce722369c5cd8adcc7c47fc1ef85b0df94eac6))
* **deps:** bump the go-dependencies group with 2 updates ([0af3f94](https://github.com/theronburger/switchyard/commit/0af3f9400fdd85ff0a62ba3fa7e3609d5ad07066))

## [v0.1.0] - 2026-08-19

### Added

- Native macOS command center and menu-bar app for isolated, configured worktree environments.
- Daemon-owned workspace preparation, ports, processes, Docker resources, health, and cleanup state.
- Safe create, adopt, archive, start, stop, rebuild, CLI, and MCP workflows.
- Codex and Claude Code Connection Doctor with managed MCP and skill installation.
- Signed Sparkle updates and a universal Homebrew Cask release path.
- App-attributed background daemon registration with scoped reload behavior when its LaunchAgent changes.
- GitHub pull-request and CI observations through the user's existing `gh` authentication.

### Security

- Authenticated loopback transport, owner-only runtime files, exact process ownership, labelled Docker cleanup, and no-follow configuration repair.
- Self-signed hardened-runtime app releases with a dedicated Ed25519 Sparkle key, checksums, SBOM, and GitHub provenance attestation.
