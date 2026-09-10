#!/bin/sh
set -eu
script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
check_directory=$(mktemp -d "${TMPDIR:-/tmp}/switchyard-swift.XXXXXX")
trap 'rm -rf "$check_directory"' EXIT HUP INT TERM
"$script_directory/build-binary.sh" "$check_directory/switchyard"
swift build --package-path "$repository_root/app" --product SwitchyardApp
SWITCHYARD_TEST_HELPER="$check_directory/switchyard" swift test --package-path "$repository_root/app"
