#!/bin/sh
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
version=$(tr -d '[:space:]' < "$repository_root/VERSION")
plist="$repository_root/packaging/Switchyard-Info.plist"

test "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$plist")" = "$version"
test "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$plist")" = "$version"
grep -Fq "## [v$version]" "$repository_root/CHANGELOG.md"
