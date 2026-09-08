# Rebuild verification

Verified on macOS with the canonical replacement source and API v3.

- `go vet ./...`, `go test ./...`, `go test -race ./...` and
  `golangci-lint run ./...` pass.
- `scripts/check-swift.sh` passes 14 native tests, including requests to a real
  freshly built Go helper, canonical fixtures, reversed replies, cancellation,
  daemon replacement, stale logs, and externally changed saved choices.
- The compiled CLI and stdio MCP start two real HTTP services in separate Git
  worktrees. Their responses identify different directories. Stopping one leaves
  the other reachable. Abrupt daemon termination stops its children; choices
  survive restart and the next Run succeeds.
- Runtime failure tests cover setup failure/retry, early service exit, readiness
  timeout, setup cancellation, forked children, foreign port collisions and health
  recovery. Explicit empty service selections do not start all services.
- Git tests preview and recheck pruning. Primary, dirty, unpushed, locked, active
  and unrelated worktrees survive. Missing registrations are removed individually.
  A large prepared worktree was removed through the native app. Removal continues
  if its client disconnects, and transport tests exercise responses beyond the
  ordinary 30-second request limit. Individual actions inspect only their selected
  worktree; inventory still reports every registered worktree.
- A freshly packaged native app was opened and operated. Existing Git worktrees
  appeared without adoption. Starting one workspace while switching to another
  kept the operation, choices and displayed run attached to their exact paths.
  Quitting the final app left its running instance reachable; the installed helper
  matched the bundled bytes. Native Codex connection setup registered the expected
  executable and API v3 root, verified independently through the Codex CLI.
- Two fresh worktrees of an actual private repository ran its web app. Verification
  included rendered welcome screens, JavaScript compilation, separate ports, and
  stopping one while the other remained reachable. The private recipe needed
  ignored environment files, explicit generated GraphQL files, and localhost links;
  those details are not embedded in product code.
- `scripts/release-checks.sh` passes. A universal arm64/x86_64 archive and SBOM were
  built with `scripts/build-release.sh`; nested code-signature and architecture
  verification passed. The established release identity, Sparkle feed/public key,
  Homebrew integration and publication checks remain in place.
- The generic boundary scan passes for source and the built application using a
  denylist kept outside the repository.

The local release packaging run used ad-hoc signing. Publisher signing, uploaded
asset verification, and a real Sparkle upgrade through the published feed require
running the protected release workflow; no release was published by this task.
The installed release and its existing running instance were left intact while
verifying the separate development installation.
