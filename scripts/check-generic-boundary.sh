#!/bin/sh
# Enforces the generic-product boundary (AGENTS.md, D-002, D-007): product
# code, tests, fixtures, documentation, bundled skills, and the built app
# bundle contain no consuming-repository identity.
#
# The identities themselves must never live in this repository, so the
# denylist is supplied from outside it:
#
#   SWITCHYARD_BOUNDARY_DENYLIST       newline-separated fixed strings
#                                      (a GitHub Actions secret in CI)
#   SWITCHYARD_BOUNDARY_DENYLIST_FILE  path to such a file outside the checkout
#
# Blank lines and lines starting with `#` are ignored. Matching is fixed-string
# and case-insensitive. Matches are reported as path:line only, never the
# matched text, so a public log cannot leak the identity either.
#
# usage: check-generic-boundary.sh [--require-denylist] [--bundle <Switchyard.app>]...
#        check-generic-boundary.sh --self-test
#
# Without --require-denylist an absent denylist is a skipped scan (a developer
# machine or a fork pull request). Release paths pass --require-denylist so a
# missing secret fails closed instead of publishing an unscanned bundle.
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")

require_denylist=0
bundles=""
self_test=0
while [ "$#" -gt 0 ]; do
	case "$1" in
		--require-denylist) require_denylist=1 ;;
		--bundle)
			shift
			test "$#" -gt 0 || { echo "--bundle requires a path" >&2; exit 2; }
			bundles="$bundles
$1"
			;;
		--self-test) self_test=1 ;;
		*) echo "usage: $0 [--require-denylist] [--bundle <app>]... | --self-test" >&2; exit 2 ;;
	esac
	shift
done

# Writes the effective denylist (one pattern per line) to $1. Prints nothing.
# Returns 1 when no denylist is configured.
load_denylist() {
	target=$1
	: > "$target"
	if [ -n "${SWITCHYARD_BOUNDARY_DENYLIST_FILE:-}" ]; then
		test -f "$SWITCHYARD_BOUNDARY_DENYLIST_FILE" || { echo "denylist file not found" >&2; return 2; }
		case "$(CDPATH= cd -- "$(dirname -- "$SWITCHYARD_BOUNDARY_DENYLIST_FILE")" && pwd)" in
			"$repository_root"|"$repository_root"/*)
				echo "the denylist file must live outside the repository checkout" >&2
				return 2
				;;
		esac
		cat "$SWITCHYARD_BOUNDARY_DENYLIST_FILE" >> "$target"
	fi
	if [ -n "${SWITCHYARD_BOUNDARY_DENYLIST:-}" ]; then
		printf '%s\n' "$SWITCHYARD_BOUNDARY_DENYLIST" >> "$target"
	fi
	grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$target" > "$target.clean" || true
	mv "$target.clean" "$target"
	test -s "$target"
}

# scan_files <denylist> <label> < newline-separated file list
# Reports path:line for every match; returns 1 when anything matched.
scan_files() {
	denylist=$1
	label=$2
	found=0
	while IFS= read -r file; do
		[ -n "$file" ] || continue
		[ -f "$file" ] || continue
		# -a treats binaries as text so Mach-O strings, plists, nibs, and
		# resources inside a bundle are all covered; -n gives the line; the
		# matched text is deliberately discarded.
		matches=$(LC_ALL=C grep -a -n -i -F -f "$denylist" -- "$file" 2>/dev/null | cut -d: -f1 || true)
		if [ -n "$matches" ]; then
			found=1
			for line in $matches; do
				echo "generic-boundary: $label match at $file:$line" >&2
			done
		fi
	done
	test "$found" -eq 0
}

scan_bundle() {
	denylist=$1
	bundle=$2
	test -d "$bundle" || { echo "bundle not found: $bundle" >&2; return 2; }
	find "$bundle" -type f | scan_files "$denylist" "bundle"
}

scan_tracked_sources() {
	denylist=$1
	(
		cd "$repository_root"
		git ls-files -z | tr '\0' '\n'
	) | (
		cd "$repository_root"
		scan_files "$denylist" "source"
	)
}

run() {
	work=$(mktemp -d "${TMPDIR:-/tmp}/switchyard-boundary.XXXXXX")
	trap 'rm -rf "$work"' EXIT HUP INT TERM
	denylist="$work/denylist"
	if ! load_denylist "$denylist"; then
		status=$?
		if [ "$status" -eq 2 ]; then
			exit 2
		fi
		if [ "$require_denylist" -eq 1 ]; then
			echo "generic-boundary: no denylist configured; refusing to continue without SWITCHYARD_BOUNDARY_DENYLIST" >&2
			exit 1
		fi
		echo "generic-boundary: no denylist configured; scan skipped" >&2
		exit 0
	fi
	pattern_count=$(wc -l < "$denylist" | tr -d ' ')
	echo "generic-boundary: scanning tracked sources against $pattern_count external pattern(s)"
	failed=0
	scan_tracked_sources "$denylist" || failed=1
	printf '%s\n' "$bundles" | while IFS= read -r bundle; do
		[ -n "$bundle" ] || continue
		echo "generic-boundary: scanning bundle $bundle"
		scan_bundle "$denylist" "$bundle" || exit 1
	done || failed=1
	if [ "$failed" -ne 0 ]; then
		echo "generic-boundary: consuming-repository identity found; see path:line above" >&2
		exit 1
	fi
	echo "generic-boundary: passed"
}

self_test() {
	work=$(mktemp -d "${TMPDIR:-/tmp}/switchyard-boundary-selftest.XXXXXX")
	trap 'rm -rf "$work"' EXIT HUP INT TERM
	denylist="$work/denylist"
	SWITCHYARD_BOUNDARY_DENYLIST='# comment

SyntheticCorpIdentity
example-consumer-repo' load_denylist "$denylist" \
		|| { echo "self-test denylist must load" >&2; exit 1; }

	mkdir -p "$work/clean.app/Contents/MacOS" "$work/dirty.app/Contents/Resources"
	printf 'generic product text\n' > "$work/clean.app/Contents/MacOS/binary"
	printf 'generic\0binary syntheticcorpidentity\0tail\n' > "$work/dirty.app/Contents/Resources/blob"

	scan_bundle "$denylist" "$work/clean.app" || { echo "clean bundle must pass" >&2; exit 1; }
	if scan_bundle "$denylist" "$work/dirty.app" 2>"$work/report"; then
		echo "bundle containing a denylisted identity must fail" >&2
		exit 1
	fi
	grep -q 'bundle match at .*blob:1$' "$work/report" || { echo "report must name path:line" >&2; exit 1; }
	if grep -qi 'syntheticcorpidentity' "$work/report"; then
		echo "report must not echo the matched identity" >&2
		exit 1
	fi

	printf '%s\n' "$work/clean.app/Contents/MacOS/binary" | scan_files "$denylist" "source" \
		|| { echo "clean source must pass" >&2; exit 1; }
	printf 'see Example-Consumer-Repo here\n' > "$work/source.md"
	if printf '%s\n' "$work/source.md" | scan_files "$denylist" "source" 2>/dev/null; then
		echo "case-insensitive source match must fail" >&2
		exit 1
	fi

	# Missing denylist: skipped by default, refused with --require-denylist.
	if ! (unset SWITCHYARD_BOUNDARY_DENYLIST SWITCHYARD_BOUNDARY_DENYLIST_FILE; load_denylist "$work/empty"); then
		:
	else
		echo "empty denylist must report absence" >&2
		exit 1
	fi
	if ! (
		unset SWITCHYARD_BOUNDARY_DENYLIST SWITCHYARD_BOUNDARY_DENYLIST_FILE
		sh "$0" --require-denylist >/dev/null 2>&1
	); then
		:
	else
		echo "--require-denylist must fail without a denylist" >&2
		exit 1
	fi
	if ! (
		unset SWITCHYARD_BOUNDARY_DENYLIST SWITCHYARD_BOUNDARY_DENYLIST_FILE
		sh "$0" >/dev/null 2>&1
	); then
		echo "absent denylist must be a skipped scan without --require-denylist" >&2
		exit 1
	fi
	# A denylist file inside the checkout is refused.
	if (SWITCHYARD_BOUNDARY_DENYLIST_FILE="$repository_root/VERSION" load_denylist "$work/inside" 2>/dev/null); then
		echo "denylist inside the checkout must be refused" >&2
		exit 1
	fi
	echo "check-generic-boundary self-test passed"
}

if [ "$self_test" -eq 1 ]; then
	self_test
	exit 0
fi
run
