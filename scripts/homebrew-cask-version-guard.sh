#!/bin/sh
# Downgrade guard for the Homebrew tap update.
#
# usage: homebrew-cask-version-guard.sh <candidate-version> <cask-path>
#   exit 0  the candidate may replace the Cask (no Cask yet, or strictly newer)
#   exit 1  refuse: the Cask exists but its version is unreadable or not older
#   homebrew-cask-version-guard.sh --self-test
set -eu

compare_versions() {
	ruby -e 'require "rubygems"; exit(Gem::Version.new(ARGV[0]) > Gem::Version.new(ARGV[1]) ? 0 : 1)' "$1" "$2"
}

cask_version() {
	sed -n 's/^  version "\([^"]*\)"/\1/p' "$1" 2>/dev/null | head -n 1
}

guard() {
	candidate=$1
	cask_path=$2
	if [ ! -e "$cask_path" ]; then
		return 0
	fi
	current=$(cask_version "$cask_path")
	if [ -z "$current" ]; then
		echo "Refusing to replace an existing Cask with an unreadable version" >&2
		return 1
	fi
	if ! compare_versions "$candidate" "$current"; then
		echo "Refusing to replace Homebrew Cask $current with non-newer release $candidate" >&2
		return 1
	fi
	return 0
}

self_test() {
	directory=$(mktemp -d "${TMPDIR:-/tmp}/switchyard-cask-guard.XXXXXX")
	trap 'rm -rf "$directory"' EXIT HUP INT TERM
	script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
	render="$script_directory/render-homebrew-cask.sh"
	sha=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef

	guard 0.2.0 "$directory/missing.rb" || { echo "missing Cask must be accepted" >&2; exit 1; }

	"$render" 0.1.0 "$sha" > "$directory/switchyard.rb"
	guard 0.2.0 "$directory/switchyard.rb" || { echo "newer release must be accepted" >&2; exit 1; }
	guard 0.1.1 "$directory/switchyard.rb" || { echo "newer patch must be accepted" >&2; exit 1; }
	guard 0.10.0 "$directory/switchyard.rb" || { echo "numeric compare must not be lexical" >&2; exit 1; }
	if guard 0.1.0 "$directory/switchyard.rb" 2>/dev/null; then echo "same version must be refused" >&2; exit 1; fi
	if guard 0.0.9 "$directory/switchyard.rb" 2>/dev/null; then echo "older version must be refused" >&2; exit 1; fi

	"$render" 0.9.0 "$sha" > "$directory/switchyard.rb"
	if guard 0.2.0 "$directory/switchyard.rb" 2>/dev/null; then echo "rollback must not go through the tap updater" >&2; exit 1; fi

	printf 'cask "switchyard" do\nend\n' > "$directory/switchyard.rb"
	if guard 0.2.0 "$directory/switchyard.rb" 2>/dev/null; then echo "unreadable version must be refused" >&2; exit 1; fi
	echo "homebrew-cask-version-guard self-test passed"
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit 0
fi
if [ "$#" -ne 2 ]; then
	echo "usage: $0 <candidate-version> <cask-path> | --self-test" >&2
	exit 2
fi
guard "$1" "$2"
