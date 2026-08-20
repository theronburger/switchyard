# Changelog

All notable changes are documented here. The project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.1.1] - 2026-08-20

### Added

- Codex local-environment integration through restart-tolerant, cached `sy prepare . --wait`, current-worktree `sy start .`, and race-safe, idempotent `sy stop . --if-running --wait` commands.

## [v0.1.0] - 2026-08-19

### Added

- Native macOS command center and menu-bar app for isolated Marketplace worktree environments.
- Daemon-owned workspace preparation, ports, processes, Docker resources, health, and cleanup state.
- Safe create, adopt, archive, start, stop, rebuild, CLI, and MCP workflows.
- Codex and Claude Code Connection Doctor with managed MCP and skill installation.
- Signed Sparkle updates and a universal Homebrew Cask release path.
- App-attributed background daemon registration with scoped reload behavior when its LaunchAgent changes.
- GitHub pull-request and CI observations through the user's existing `gh` authentication.

### Security

- Authenticated loopback transport, owner-only runtime files, exact process ownership, labelled Docker cleanup, and no-follow configuration repair.
- Self-signed hardened-runtime app releases with a dedicated Ed25519 Sparkle key, checksums, SBOM, and GitHub provenance attestation.
