# Security policy

## Supported versions

Security fixes are provided for the latest released minor version. Source snapshots and pre-release builds are supported on a best-effort basis.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability and do not include credentials, account identifiers, private repository contents, environment files, or raw service logs in a report.

Use [GitHub private vulnerability reporting](https://github.com/theronburger/switchyard/security/advisories/new). Include the affected Switchyard version, macOS version and architecture, impact, safe reproduction steps, and any proposed mitigation. Use synthetic repositories, services, and credentials only.

## Release trust

Switchyard does not currently have an Apple Developer identity and is not notarized. Official app archives use the persistent self-signed `Theron Burger Apps Release` identity with the hardened runtime. The Swift frontend alone carries `com.apple.security.cs.disable-library-validation` because a self-signed app has no Apple Team ID for Sparkle framework library validation; the daemon and CLI never carry that entitlement.

Sparkle verifies the archive and signed feed with Switchyard's dedicated Ed25519 key before extraction. Every GitHub Release also includes SHA-256 checksums, a CycloneDX SBOM for the Go runtime dependency graph, and a GitHub build-provenance attestation. Swift packages are pinned separately in `app/Package.resolved`.

Verify a downloaded release:

```bash
shasum -a 256 -c checksums.txt
codesign --verify --deep --strict --verbose=2 "Switchyard.app"
gh attestation verify switchyard_X.Y.Z_macos_universal.zip --repo theronburger/switchyard
```

Homebrew 6 requires a separate, explicit Gatekeeper post-processing step for this self-signed app. The documented install command removes quarantine only from `/Applications/Switchyard.app`; the Cask does not hide that action in postflight.
