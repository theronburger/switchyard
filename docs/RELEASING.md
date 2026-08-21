# Release runbook

## Release trust

Switchyard has no Apple Developer account. Release bundles use the persistent self-signed publisher identity `Theron Burger Apps Release`, so they cannot be notarized. The stable identity provides continuity across releases; Sparkle separately authenticates every update archive with a dedicated Ed25519 key, carried as an item signature in the appcast, before extraction.

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
- `SWITCHYARD_BOUNDARY_DENYLIST`: the newline-separated consuming-repository identities (names, hosts, paths, tickets prefixes) that must never appear in product code or the built bundle. `scripts/check-generic-boundary.sh --require-denylist` fails closed when it is absent, so the release cannot publish an unscanned bundle. The list lives only in this secret (and in the maintainer's private notes); it is never committed, and the scanner reports `path:line` without echoing the matched text.

Two secrets are repository-wide because the workflows that need them run without the protected environment:

- `RELEASE_PLEASE_TOKEN` (Actions secret): a fine-grained personal access token restricted to `theronburger/switchyard` with **Contents: read and write** and **Pull requests: read and write**. Release Please uses it to open the release pull request and push the version tag. The default `GITHUB_TOKEN` cannot start other workflows, so a release pull request it opened would have no CI checks and a tag it pushed would never trigger `release.yml`. `release-please.yml` fails immediately when the secret is missing. Rotate it before expiry like the other release state.
- `SWITCHYARD_BOUNDARY_DENYLIST` (Actions secret, same value as the environment copy): lets CI enforce the generic boundary on every push and same-repository pull request, including the dry-run bundle. Fork pull requests receive no secrets and get a skipped, non-enforced scan; the enforced scan runs when the change lands.

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

## Version ownership

Release Please is the only writer of release versions. One release pull request updates, in lockstep:

- `VERSION` (the `simple` strategy's version file);
- `.release-please-manifest.json`;
- `CHANGELOG.md`, where it inserts `## [X.Y.Z](compare-link) (date)` above the newest version heading;
- the two lines in `packaging/Switchyard-Info.plist` annotated with `<!-- x-release-please-version -->`. The generic updater rewrites only annotated lines; removing an annotation silently freezes the plist and `scripts/check-version.sh` then blocks the release.

Everything else derives from `VERSION` at build time: the Go `-ldflags` version, the bundle's `CFBundleShortVersionString`/`CFBundleVersion`, the archive name, the Cask `version`, and the appcast `sparkle:version`. The app refuses to install a bundled daemon whose `version` output differs from its own `CFBundleShortVersionString`, and every client requires an exact daemon version match, so a drift anywhere surfaces as a handshake error instead of a silent mismatch.

Rules that follow from this:

- Never edit `VERSION`, the manifest, or the plist version lines by hand. Never add an `Unreleased` section to `CHANGELOG.md`; Release Please would insert the new version below it.
- Commits that reach `main` must be Conventional Commits. A non-conventional commit (for example a `wip:` subject) is invisible to Release Please and neither bumps the version nor appears in the notes. Add its notes by editing the release pull request body before merge.
- Before 1.0.0, `bump-minor-pre-major` keeps a `feat!:` or `BREAKING CHANGE` footer at a minor bump. Cutting 1.0.0 is a deliberate `Release-As: 1.0.0` footer, not a side effect.
- `scripts/release-checks.sh` (`make release-checks`) verifies all of the above plus the Cask template, entitlements, Sparkle plist keys, Release Please configuration (including `draft` and `force-tag-creation`, because a draft release creates no tag unless forced and the tag is what triggers publication), workflow wiring, the single publication entry point, and the publication order. It runs inside `scripts/ci.sh`, so CI and the release workflow both execute it before any secret is imported.

## Generic-boundary scan

`scripts/check-generic-boundary.sh` enforces the AGENTS.md invariant that product code, tests, fixtures, docs, bundled skills, and the shipped bundle contain no consuming-repository identity. The identities are supplied from outside the repository (`SWITCHYARD_BOUNDARY_DENYLIST`, or `SWITCHYARD_BOUNDARY_DENYLIST_FILE` pointing outside the checkout) and matched as case-insensitive fixed strings across every tracked file and, with `--bundle`, every file inside `Switchyard.app` including Mach-O strings and resources. A match is reported as `path:line` only. CI runs the enforced scan on pushes and same-repository pull requests and scans the dry-run bundle; the release workflow scans tracked sources before any secret is imported and scans the freshly built bundle before upload. `--require-denylist` makes an absent list a failure, and both CI and release pass it; locally, `make release-checks` runs the scanner's self-test and the scan is skipped unless you export the list.

## Cut a release

1. Merge conventional commits to `main`. Release Please maintains the version, changelog, manifest, and plist in one release pull request, acting with `RELEASE_PLEASE_TOKEN` so the pull request runs CI like any other.
2. Approve and merge the green Release Please pull request.
3. The merge creates the draft release and pushes the version tag; the tag push triggers the protected release workflow. Approve the `release` environment only after its tag and generated files match the reviewed release pull request.
4. The release workflow reruns `make check`, `make race`, the exact linters, vulnerability scan, and release dry run on macOS.
   It renders the candidate Cask and validates it with the runner's Homebrew before anything is published: `scripts/validate-homebrew-cask.sh` performs Ruby parsing, a trusted `brew install --cask --dry-run`, and `brew style`. Locally, run the same script and `brew audit`; if this work Mac cannot fetch Homebrew's portable Ruby because of its known RubyGems TLS failure, set `SWITCHYARD_SKIP_BREW_STYLE=1` locally and record that environmental failure separately from successful Cask parse and dry-run checks.
5. Run the secret, history, configuration, binary-artifact, and hostile Fable reviews before merging the release pull request.

`release.yml` has exactly one trigger, the `v*` tag push. Release Please pushes that tag with `RELEASE_PLEASE_TOKEN`, and a deliberately hand-pushed tag takes the same path. There is intentionally no `workflow_call` entry point: with a token that starts workflows, calling the release workflow from `release-please.yml` as well would publish the same tag twice; `scripts/release-checks.sh` rejects such a call.

The tag workflow, in order: reruns production checks; enforces the generic boundary on tracked sources; validates the rendered Cask; empties `dist/`; imports the publisher identity into an ephemeral runner Keychain; builds Intel and Apple Silicon binaries and a universal app; scans the built bundle against the denylist; signs nested Sparkle components and the daemon in strict order; verifies entitlements and architectures; derives the Sparkle public key from the private seed and compares it with the bundle; performs a real signed launch; generates the appcast from a directory containing only this release's archive and verifies its Ed25519 signature cryptographically against the bundle's `SUPublicEDKey`; produces checksums and a CycloneDX SBOM; attests the artifacts; uploads the exact-named assets to the still-draft release; downloads them back and re-verifies checksums, byte equality, the signature, and the code signature of the unpacked app; only then marks the release published and `latest` (which arms `releases/latest/download/appcast.xml`); and finally updates the Homebrew Cask behind the downgrade guard. Publication never uses a glob, so a stale archive in the output directory cannot be selected. Swift packages remain pinned in `app/Package.resolved`.

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

`generate_appcast` only warns, and writes an unsigned item, when the supplied private key does not match the app's embedded `SUPublicEDKey`; it never emits a `sparkle-signatures` comment. The workflow therefore derives the public key from the seed and compares it with the bundle before generating the appcast, and `scripts/verify-appcast.sh` refuses an item without `sparkle:edSignature`.

`scripts/verify-appcast.sh` runs in the release workflow against the generated `appcast.xml` and again against the assets downloaded back from the draft release, and can be run against a published feed as well. It proves the single item advertises the exact version, the tagged GitHub asset URL, an Ed25519 signature, and the app's minimum system version. With `--archive <zip> --public-key <SUPublicEDKey>` it also proves the enclosure length equals the archive size and that the signature verifies cryptographically over the archive bytes (`scripts/verify-sparkle-signature.swift`, CryptoKit Ed25519), so a feed signed with the wrong seed is refused before it is armed. Download the feed and archive to files before verifying: the script reads the appcast several times, so `/dev/stdin` or a pipe is exhausted after the first check.

Confirm that the app installs the bundled daemon, the generated LaunchAgent includes `AssociatedBundleIdentifiers = [com.theronburger.switchyard]`, Connection Doctor can inspect and repair detected agents, `sy doctor` passes, the Cask renders the expected release URL and checksum, and **Check for Updates…** reads the signed appcast. A changed LaunchAgent plist must boot out and bootstrap only `com.theronburger.switchyard.daemon`; a helper-only update uses the scoped kickstart path.

On `ssh m4`, verify the published install with read-only `sfltool dumpbtm` output and the visible Login Items settings pane. Never use `sfltool resetbtm`. Because the self-signed publisher has no Team Identifier, treat this on-machine attribution check as required rather than inferring attribution from signing metadata.

## Roll back a bad release

Rollback never deletes or rewrites a tag, release, or asset. It moves the `latest` pointer and the Cask:

```bash
scripts/release-rollback.sh 0.1.0          # prints the complete plan
scripts/release-rollback.sh 0.1.0 --apply  # re-points GitHub "latest" after verifying the release's signed assets
```

`--apply` refuses a draft or asset-less release, downloads that release's `appcast.xml`, archive, and `checksums.txt` to a temporary directory (never streaming through stdin), checks the checksums, verifies the appcast metadata and Ed25519 signature against the `SUPublicEDKey` in `packaging/Switchyard-Info.plist`, and only then re-points `latest`. Every `gh` call reads from `/dev/null` so nothing can wait on an interactive prompt.

What each layer does during and after a rollback:

- **Sparkle.** `SUFeedURL` resolves `releases/latest/download/appcast.xml`, so re-pointing `latest` immediately stops every installed app from being offered the bad version. Sparkle never downgrades: machines that already installed the bad version stay there until a higher fix-forward version ships. Do not try to ship a "rollback" appcast with a lower version.
- **Homebrew.** The release workflow's downgrade guard (`scripts/homebrew-cask-version-guard.sh`) refuses to write an older version into the tap, so a tap rollback is a deliberate, reviewed `git revert` pushed to `theronburger/homebrew-tap`. Because the Cask sets `auto_updates true`, `brew upgrade` does not touch Sparkle-updated installs unless `--greedy` is used.
- **Daemon.** The app installs its bundled daemon by content digest, not by version ordering, and reloads only its own LaunchAgent. Reinstalling an older app therefore restores the matching older daemon on next launch; nothing else on the machine is touched.
- **Visibility.** Mark the bad release as a pre-release and annotate its notes instead of deleting it so checksums, the SBOM, and the provenance attestation stay auditable.

## Uninstall ownership

`brew uninstall --cask switchyard` removes only Switchyard-owned items, in Homebrew's fixed directive order:

1. `early_script` quits the app through an inline JXA script that terminates every running instance of `com.theronburger.switchyard` and waits for exit. It runs before `launchctl` because the running app owns daemon installation and would re-register the LaunchAgent. The script is inline so uninstall still works when the bundle was moved or deleted by hand.
2. `launchctl` boots out `com.theronburger.switchyard.daemon` and removes its plist.
3. `quit` is a second, idempotent stop for the app.
4. `delete` removes the app-installed daemon binary under `~/Library/Application Support/Switchyard/bin/switchyard` and the LaunchAgent plist, so nothing can recreate the job mid-uninstall.

`--zap` additionally trashes `~/Library/Application Support/Switchyard` (which contains the accepted private profiles and runtime state) and the app's preferences. Worktrees, repositories, Docker resources, the Homebrew `sy` symlink's target bundle, MCP registrations, and managed skills are never touched by the Cask. `scripts/release-checks.sh` fails if the rendered Cask references any path outside that ownership set or adds a flight hook.

## Uninstall verification

MCP removal remains explicit because agent hosts own their configuration:

```bash
codex mcp remove switchyard
claude mcp remove switchyard --scope user
brew uninstall --cask switchyard
```

Managed skills remain by design. Every tree Switchyard installs carries an owner-only `.switchyard-managed-skill` marker. An explicit repair replaces only a marked tree (or an unmarked tree byte-identical to the bundled release, which it adopts), including local edits inside it. A `switchyard` skill directory the user authored themselves is never moved, renamed, or deleted: Connection Doctor reports it as refused with its path and asks the user to move it aside deliberately.
