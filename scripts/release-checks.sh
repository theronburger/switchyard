#!/bin/sh
# Repository-local release path verification. Needs no credentials, network,
# signing identity, or Sparkle key, so it runs in CI, in the release workflow
# before any secret is touched, and on a developer machine via `make release-checks`.
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
cd "$repository_root"

version=$(tr -d '[:space:]' < VERSION)
placeholder_sha=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef

echo "release-checks: version propagation"
"$script_directory/check-version.sh"

echo "release-checks: script self-tests"
"$script_directory/check-pr-title.sh" --self-test
"$script_directory/homebrew-cask-version-guard.sh" --self-test
"$script_directory/verify-appcast.sh" --self-test
"$script_directory/check-generic-boundary.sh" --self-test

echo "release-checks: Info.plist update configuration"
plist=packaging/Switchyard-Info.plist
plutil -lint "$plist" >/dev/null
test "$(/usr/libexec/PlistBuddy -c 'Print :SUFeedURL' "$plist")" = \
	"https://github.com/theronburger/switchyard/releases/latest/download/appcast.xml"
test "$(/usr/libexec/PlistBuddy -c 'Print :SUPublicEDKey' "$plist")" = "F/7KIoRQOG3oCqMUkAOmnxuW/0XZ2IE9sinDFCvNrYg="
test "$(/usr/libexec/PlistBuddy -c 'Print :SUVerifyUpdateBeforeExtraction' "$plist")" = "true"
test "$(/usr/libexec/PlistBuddy -c 'Print :SUAutomaticallyUpdate' "$plist")" = "false"
test "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$plist")" = "com.theronburger.switchyard"
test "$(/usr/libexec/PlistBuddy -c 'Print :SwitchyardChannel' "$plist")" = "release"
grep -Fq 'F/7KIoRQOG3oCqMUkAOmnxuW/0XZ2IE9sinDFCvNrYg=' docs/RELEASING.md

echo "release-checks: entitlements"
entitlement_keys=$(/usr/libexec/PlistBuddy -c 'Print' packaging/Switchyard.entitlements | grep -c '= ' || true)
test "$entitlement_keys" -eq 1
test "$(/usr/libexec/PlistBuddy -c 'Print :com.apple.security.cs.disable-library-validation' packaging/Switchyard.entitlements)" = "true"

echo "release-checks: Homebrew Cask template"
rendered=$(mktemp "${TMPDIR:-/tmp}/switchyard-cask.XXXXXX")
trap 'rm -f "$rendered"' EXIT HUP INT TERM
"$script_directory/render-homebrew-cask.sh" "$version" "$placeholder_sha" > "$rendered"
ruby -c "$rendered" >/dev/null
grep -Fq "version \"$version\"" "$rendered"
grep -Fq "sha256 \"$placeholder_sha\"" "$rendered"
grep -Fq 'releases/download/v#{version}/switchyard_#{version}_macos_universal.zip' "$rendered"
grep -Fq 'depends_on macos: :sequoia' "$rendered"
grep -Fq 'auto_updates true' "$rendered"
grep -Fq 'launchctl:    "com.theronburger.switchyard.daemon"' "$rendered"
grep -Fq 'quit:         "com.theronburger.switchyard"' "$rendered"
grep -Fq '"~/Library/Application Support/Switchyard/bin/switchyard"' "$rendered"
grep -Fq '"~/Library/LaunchAgents/com.theronburger.switchyard.daemon.plist"' "$rendered"
# The caveats heredoc may instruct the user; stanzas must never act on quarantine.
if sed '/caveats <<~EOS/,/^  EOS/d' "$rendered" | grep -Eq 'postflight|preflight|xattr|quarantine'; then
	echo "the Cask must not remove quarantine or add a flight hook" >&2
	exit 1
fi
# Uninstall and zap must only touch Switchyard-owned paths.
if grep -E '^\s*"~/' "$rendered" | grep -Ev '"~/Library/(Application Support/Switchyard|LaunchAgents/com\.theronburger\.switchyard\.daemon\.plist|Preferences/com\.theronburger\.switchyard\.plist)' ; then
	echo "the Cask references a path outside Switchyard ownership" >&2
	exit 1
fi
if grep -Fq 'quit-switchyard.js' "$rendered"; then
	echo "the Cask uninstall must not depend on a file inside the app bundle" >&2
	exit 1
fi

echo "release-checks: LaunchAgent label and bundle identity agree across Swift and Cask"
grep -Fq '"com.theronburger.switchyard.daemon"' app/Sources/SwitchyardKit/Model/SwitchyardChannel.swift
grep -Fq '"com.theronburger.switchyard"' app/Sources/SwitchyardKit/Model/SwitchyardChannel.swift
grep -Fq 'Switchyard.app/Contents/Resources/SwitchyardDaemon' "$rendered"

echo "release-checks: Release Please configuration"
python3 - <<'PY'
import json, sys
config = json.load(open("release-please-config.json"))["packages"]["."]
manifest = json.load(open(".release-please-manifest.json"))
assert config["release-type"] == "simple", "simple strategy owns VERSION"
assert config["version-file"] == "VERSION"
assert config["include-v-in-tag"] is True, "Release workflow and SUFeedURL expect v-prefixed tags"
assert config["include-component-in-tag"] is False
assert config["draft"] is True, "the release workflow publishes the draft after verification"
assert config["force-tag-creation"] is True, "a draft release creates no tag unless tag creation is forced; the tag triggers release.yml"
assert config["bump-minor-pre-major"] is True, "a breaking change must not cut 1.0.0 by accident"
assert any(f.get("path") == "packaging/Switchyard-Info.plist" for f in config["extra-files"])
assert manifest["."] == open("VERSION").read().strip()
PY

echo "release-checks: workflow wiring"
grep -Fq 'artifact-metadata: write' .github/workflows/release.yml
grep -Fq 'environment: release' .github/workflows/release.yml
grep -Fq 'scripts/homebrew-cask-version-guard.sh' .github/workflows/release.yml
grep -Fq 'scripts/verify-appcast.sh' .github/workflows/release.yml
grep -Fq 'scripts/check-version.sh' .github/workflows/release.yml
grep -Fq 'scripts/validate-homebrew-cask.sh' .github/workflows/release.yml
grep -Fq -- '--public-key' .github/workflows/release.yml
# Release Please must act with a token that starts workflows, and exactly one
# entry point (the tag push) may publish a version.
grep -Fq 'token: ${{ secrets.RELEASE_PLEASE_TOKEN }}' .github/workflows/release-please.yml
grep -Fq 'RELEASE_PLEASE_TOKEN' docs/RELEASING.md
if grep -Fq 'workflow_call' .github/workflows/release.yml || grep -Fq 'uses: ./.github/workflows/release.yml' .github/workflows/release-please.yml; then
	echo "release.yml must be triggered only by the tag push; a workflow_call path publishes twice" >&2
	exit 1
fi

echo "release-checks: generic-boundary scan wiring"
grep -Fq 'scripts/check-generic-boundary.sh --require-denylist' .github/workflows/ci.yml
grep -Fq 'scripts/check-generic-boundary.sh --require-denylist --bundle dist/Switchyard.app' .github/workflows/ci.yml
grep -Fq 'scripts/check-generic-boundary.sh --require-denylist' .github/workflows/release.yml
grep -Fq 'scripts/check-generic-boundary.sh --require-denylist --bundle dist/Switchyard.app' .github/workflows/release.yml
grep -Fq 'SWITCHYARD_BOUNDARY_DENYLIST' docs/RELEASING.md

echo "release-checks: publication order"
python3 - <<'PY'
import re
workflow = open(".github/workflows/release.yml").read()
names = re.findall(r"^\s+- name: (.+)$", workflow, re.M)
def index(prefix):
    matches = [i for i, n in enumerate(names) if n.startswith(prefix)]
    assert len(matches) == 1, f"expected exactly one step starting with {prefix!r}, found {matches}"
    return matches[0]
cask = index("Validate rendered Homebrew Cask")
build = index("Build self-signed universal release")
bundle_scan = index("Enforce generic boundary on the built bundle")
upload = index("Upload assets to the draft release")
verify = index("Verify uploaded assets before publication")
publish = index("Publish release and arm the appcast")
tap = index("Update Homebrew Cask")
assert cask < upload, "the Cask must validate before anything is published"
assert build < bundle_scan < upload, "the built bundle must pass the boundary scan before upload"
assert upload < verify < publish < tap, "verification must precede marking latest, and latest must precede the tap update"
publish_step = workflow.split("- name: Publish release and arm the appcast")[1].split("- name:")[0]
assert "--latest" in publish_step and "--draft=false" in publish_step
upload_step = workflow.split("- name: Upload assets to the draft release")[1].split("- name:")[0]
assert "--latest" not in upload_step and "--draft=false" not in upload_step, "upload must keep the release a draft"
assert "dist/*.zip" not in workflow, "publication must name the exact release archive, never a glob"
PY
if [ -x "$(go env GOPATH)/bin/actionlint" ]; then
	"$(go env GOPATH)/bin/actionlint"
elif command -v actionlint >/dev/null 2>&1; then
	actionlint
else
	echo "release-checks: actionlint not installed; skipping workflow lint" >&2
fi

echo "release-checks: passed"
