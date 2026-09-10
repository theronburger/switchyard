# Switchyard 0.3.0

Switchyard now has a smaller execution engine built around one job: run multiple
Git worktrees side by side without making you manage ownership or adoption.

Run includes setup, service preparation, isolated ports and readiness. Progress,
logs, Stop and retry stay attached to the exact workspace. Git supplies the
worktree inventory; pruning previews and rechecks the selected directory.

The native UI, agent connections, Homebrew distribution and signed Sparkle updates
remain. Configuration opens in your editor through three icon buttons; valid saves
apply automatically to future runs.

The update installs the matching helper and repairs its LaunchAgent automatically.
Prepared private configuration migrates on first launch, preserving previous files
and saved workspace choices. Existing runs stop before the helper changes. Runtime
state is rebuilt rather than carried forward. The private upgrade recipe must be
staged and verified before updating a legacy installation; see the release runbook.

A configured service still needs its own environment values and toolchain. Startup
errors remain visible in Run output; a catalog entry is not a claim that every
service is ready for every target.
