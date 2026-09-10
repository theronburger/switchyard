# Decisions

These are the current product decisions. Earlier designs remain in Git history;
they do not define the replacement engine.

1. **Git is the worktree inventory.** Every registered worktree of a configured
   repository is visible. There is no Switchyard adoption, owner, or occupancy state.
2. **Run includes setup.** Private setup commands prepare the directory; selected
   service preparation runs next, followed by the services and readiness checks.
   Repository tools handle dependency and build caches.
3. **One daemon runs the commands.** The native app installs and starts it. The
   app, CLI and MCP use the same small local API. MCP contains no lifecycle logic.
4. **Save accepts configuration.** Repository commands stay in private config.json
   outside consuming repositories. Target and service choices are saved by path.
   There is no configuration compiler, accepted revision graph, or setup journal.
5. **Runtime state is disposable.** Runs live in memory. Closing the app leaves
   them running. Restarting the helper stops them; the next Run prepares normally.
   Choices survive. No SQLite runtime database or process adoption is required.
6. **A run can stop only its children.** Each command has a child supervisor in
   its own process group. Loss of the daemon's pipe stops that group and descendants.
   No executable-name kills, global Docker cleanup, or guessed process identity.
7. **Prune means one exact worktree.** Preview its path and Git blockers, recheck,
   then call Git without force. Preserve primary, dirty, unpushed, locked, active
   and unrelated worktrees. There is no reset/clean shortcut.
8. **Native design and distribution remain product features.** Preserve sidebar
   navigation, useful controls, agent links, branding, signed universal bundles,
   Homebrew installation and Sparkle updates. The release bundle identity, feed
   and signing key remain unchanged. Development has its own root and identity.
9. **Prove behavior.** Real processes, Git repositories, transport tests and a
   freshly built native app verify the implementation. HTTP readiness alone does
   not prove that a consuming app rendered correctly; browser verification matters.
