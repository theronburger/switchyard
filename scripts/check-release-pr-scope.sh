#!/bin/sh
set -eu

expected_paths='.release-please-manifest.json
CHANGELOG.md
VERSION
packaging/Switchyard-Info.plist'

if [ "${1:-}" = "--self-test" ]; then
	actual=$(printf '%s\n' "$expected_paths" | LC_ALL=C sort)
	test "$actual" = "$expected_paths"
	case "docs/RELEASING.md" in
		.release-please-manifest.json|CHANGELOG.md|VERSION|packaging/Switchyard-Info.plist) exit 1 ;;
	esac
	exit 0
fi

if [ "$#" -ne 2 ]; then
	echo "usage: $0 <base-revision> <head-revision>" >&2
	exit 2
fi

base_revision=$1
head_revision=$2
git rev-parse --verify "$base_revision^{commit}" >/dev/null
git rev-parse --verify "$head_revision^{commit}" >/dev/null

actual_paths=$(git diff --name-only --diff-filter=ACMRTUXB "$base_revision" "$head_revision" | LC_ALL=C sort)
if [ "$actual_paths" != "$expected_paths" ]; then
	echo "a release commit must change exactly the four Release Please files" >&2
	echo "expected:" >&2
	printf '%s\n' "$expected_paths" >&2
	echo "actual:" >&2
	printf '%s\n' "$actual_paths" >&2
	exit 1
fi
