# Release runbook

## Release trust

Switchyard has no Apple Developer account. Release bundles use the persistent self-signed publisher identity `Theron Burger Apps Release`, so they cannot be notarized. The stable identity provides continuity across releases; Sparkle separately authenticates every update archive and appcast with a dedicated Ed25519 key before extraction.

The frontend alone carries `com.apple.security.cs.disable-library-validation`. This narrowly addresses Sparkle framework loading when a self-signed app has no Team ID. Never apply it to `SwitchyardDaemon`, the CLI surface, or any future privileged helper.

Installation must keep Gatekeeper acknowledgement explicit and limited to the exact app bundle:

```bash
brew tap theronburger/tap && brew trust --cask theronburger/tap/switchyard && brew install --cask switchyard && xattr -dr com.apple.quarantine "/Applications/Switchyard.app" && open -a "Switchyard"
```

Do not add quarantine removal to a Cask postflight.

The Cask's `depends_on macos: :sequoia` is a minimum, not an exact pin: Homebrew 6's `DependsOn#macos=` parses this form with the `>=` comparator. Keep the bare-symbol form used by current Homebrew rather than converting it to a string comparison.

## Protected GitHub environment

The `release` environment requires Theron as a reviewer. Store these values as environment secrets, never repository-wide secrets:

- `SWITCHYARD_CERTIFICATE_BASE64`: encrypted PKCS#12 publisher identity encoded for GitHub Actions.
- `SWITCHYARD_CERTIFICATE_PASSWORD`: the PKCS#12 password.
- `SWITCHYARD_SIGNING_IDENTITY`: `Theron Burger Apps Release`.
- `SPARKLE_PRIVATE_KEY`: the private seed for account `com.theronburger.switchyard`.
- `HOMEBREW_TAP_DEPLOY_KEY`: a disposable SSH private key whose public half has write access only to `theronburger/homebrew-tap`.

GitHub secrets are deployment copies, not backups. Keep the publisher identity and Switchyard Sparkle item in the login Keychains on this Mac and `ssh m4`.

## Publishing-key ownership

- Publisher identity: `Theron Burger Apps Release`, SHA-256 certificate fingerprint `C972D3B2E8DEA42A078BA464ADFC43C95B18A0F6EEEF022A7588B9C954D11F08`.
- Sparkle account: `com.theronburger.switchyard`.
- Sparkle service: `https://sparkle-project.org`.
- Embedded Sparkle verifier: `F/7KIoRQOG3oCqMUkAOmnxuW/0XZ2IE9sinDFCvNrYg=`.

The Sparkle private seed is irreplaceable after the first public release: an install trusts future updates only when their signatures derive from the embedded public key. Rotate it only by first shipping an old-key-signed update that embeds the replacement public key.

The publisher certificate and private key are also long-lived release state. Before its expiry, ship and verify a controlled identity transition. Keep encrypted recovery copies of the publisher identity, its password, and the Sparkle seed outside the repository and release artifacts.

Never print a private key, seed, certificate password, or exported PKCS#12 content into a terminal transcript, CI log, issue, pull request, or repository file.

## First-publication history gate

The original private history contains personal paths and production-only visual fixtures. Deleting them at the tip is insufficient. Before changing repository visibility, rewrite every branch and tag that will remain on GitHub so those paths and values are absent from every reachable commit, then force-replace the still-private remote refs. Delete obsolete remote branches and tags rather than publishing them.

Create an offline backup before rewriting. Remove the historical `design-qa/` captures and the dated private golden-run record, replace personal absolute paths and real fixture metadata with synthetic values, and run `gitleaks git . --redact --exit-code 1 --no-banner` against the exact rewritten refs. Inspect the GitHub branch and tag lists after the force update. Only then may the repository become public and the release pull request be pushed. Never publish a backup ref or bundle.

History-rewrite replacement maps and scanner JSON reports may themselves retain private source values or author metadata. Keep them outside the repository while the rewrite is active, then securely dispose of every temporary copy before the visibility change; ignored `dist/` files are not an acceptable archive.

## Cut a release

1. Merge conventional commits to `main`. Release Please maintains the version, changelog, and plist in one release pull request.
2. Approve and merge the green Release Please pull request. Native `GITHUB_TOKEN` pull requests may require the repository's configured Actions approval before their checks run.
3. The merge creates the version tag and draft release, then calls the protected reusable release workflow. Approve the `release` environment only after its tag and generated files match the reviewed release pull request.
4. The release workflow reruns `make check`, `make race`, the exact linters, vulnerability scan, and release dry run on macOS.
   Render the candidate Cask and validate it with the current Homebrew: Ruby parsing, `brew install --cask --dry-run`, and `brew fetch --cask`. Run `brew audit` as well; if this work Mac cannot fetch Homebrew's portable Ruby because of its known RubyGems TLS failure, record that environmental failure separately from successful Cask parse and fetch checks.
5. Run the secret, history, configuration, binary-artifact, and hostile Fable reviews before merging the release pull request.

The tag workflow reruns production checks, imports the publisher identity into an ephemeral runner Keychain, builds Intel and Apple Silicon binaries, creates a universal app, signs nested Sparkle components and the daemon in strict order, verifies entitlements and architectures, derives the Sparkle public key from the private seed, performs a real signed launch, produces checksums and a CycloneDX SBOM for the Go runtime dependency graph, signs the appcast, attests the artifacts, publishes the GitHub Release, and updates the Homebrew Cask with a downgrade guard. Swift packages remain pinned in `app/Package.resolved`.

## Verify a published release

```bash
gh release download vX.Y.Z --repo theronburger/switchyard
shasum -a 256 -c checksums.txt
codesign --verify --deep --strict --verbose=2 "Switchyard.app"
gh attestation verify switchyard_X.Y.Z_macos_universal.zip --repo theronburger/switchyard
brew tap theronburger/tap
brew trust --cask theronburger/tap/switchyard
brew install --cask switchyard
xattr -dr com.apple.quarantine "/Applications/Switchyard.app"
open -a "Switchyard"
```

Confirm that the app installs the bundled daemon, the generated LaunchAgent includes `AssociatedBundleIdentifiers = [com.theronburger.switchyard]`, Connection Doctor can inspect and repair detected agents, `sy doctor` passes, the Cask renders the expected release URL and checksum, and **Check for Updates…** reads the signed appcast. A changed LaunchAgent plist must boot out and bootstrap only `com.theronburger.switchyard.daemon`; a helper-only update uses the scoped kickstart path.

On `ssh m4`, verify the published install with read-only `sfltool dumpbtm` output and the visible Login Items settings pane. Never use `sfltool resetbtm`. Because the self-signed publisher has no Team Identifier, treat this on-machine attribution check as required rather than inferring attribution from signing metadata.

## Uninstall verification

Homebrew uninstall must stop the running app before unregistering `com.theronburger.switchyard.daemon`, then remove the installed daemon binary and LaunchAgent plist so the app cannot recreate the job mid-uninstall. MCP removal remains explicit because agent hosts own their configuration:

```bash
codex mcp remove switchyard
claude mcp remove switchyard --scope user
brew uninstall --cask switchyard
```

Managed skills remain by design. Connection Doctor explains that an explicit repair replaces the managed skill tree with the bundled release, including local edits.
