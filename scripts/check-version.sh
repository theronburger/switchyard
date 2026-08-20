#!/bin/sh
# Verifies that every version-bearing release input agrees with VERSION.
#
# Release Please owns VERSION, CHANGELOG.md, .release-please-manifest.json, and
# the annotated lines in packaging/Switchyard-Info.plist. This check fails when
# any of those drift, so a release pull request cannot merge with a stale plist
# or an unannotated version line that Release Please would silently skip.
#
# usage: check-version.sh [expected-version]
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
version=$(tr -d '[:space:]' < "$repository_root/VERSION")
plist="$repository_root/packaging/Switchyard-Info.plist"
manifest="$repository_root/.release-please-manifest.json"
config="$repository_root/release-please-config.json"
changelog="$repository_root/CHANGELOG.md"

fail() {
	echo "check-version: $*" >&2
	exit 1
}

escape_regex() {
	printf '%s' "$1" | sed 's/[.[\*^$]/\\&/g'
}

case "$version" in
	[0-9]*.[0-9]*.[0-9]*) ;;
	*) fail "VERSION must be a bare semantic version without a v prefix: $version" ;;
esac

if [ "$#" -ge 1 ] && [ "$1" != "$version" ]; then
	fail "expected version $1 but VERSION contains $version"
fi

test "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$plist")" = "$version" \
	|| fail "CFBundleShortVersionString does not match VERSION"
test "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$plist")" = "$version" \
	|| fail "CFBundleVersion does not match VERSION"

# Release Please's generic updater only rewrites lines carrying this marker.
annotated_version_lines=$(grep -c 'x-release-please-version' "$plist" || true)
test "$annotated_version_lines" -eq 2 \
	|| fail "Info.plist must annotate exactly two version lines with x-release-please-version (found $annotated_version_lines)"
for key in CFBundleShortVersionString CFBundleVersion; do
	grep -A1 "<key>$key</key>" "$plist" | grep -Fq 'x-release-please-version' \
		|| fail "$key is not annotated for Release Please"
done

grep -Eq "\"\\.\": *\"$(escape_regex "$version")\"" "$manifest" \
	|| fail ".release-please-manifest.json does not record $version"
grep -Fq '"version-file": "VERSION"' "$config" \
	|| fail "release-please-config.json must bump the VERSION file"
grep -Fq '"path": "packaging/Switchyard-Info.plist"' "$config" \
	|| fail "release-please-config.json must list the Info.plist as a generic extra file"

# Accept both the hand-written "## [v0.1.0] - date" form used before Release
# Please and the "## [0.2.0](compare-link) (date)" form Release Please writes.
grep -Eq "^## \\[v?$(escape_regex "$version")\\]" "$changelog" \
	|| fail "CHANGELOG.md has no heading for $version"
if grep -Eq '^## \[?Unreleased\]?' "$changelog"; then
	fail "CHANGELOG.md must not contain an Unreleased section; Release Please inserts entries above the newest version heading"
fi
