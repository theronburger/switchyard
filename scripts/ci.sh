#!/bin/sh
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/switchyard-ci.XXXXXX")
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM

cd "$repository_root"
"$script_directory/release-checks.sh"
unformatted_files=$(gofmt -l $(rg --files cmd internal -g '*.go'))
if [ -n "$unformatted_files" ]; then
	echo "gofmt required for:" >&2
	echo "$unformatted_files" >&2
	exit 1
fi
go mod tidy -diff
go vet ./...
go test -race ./...
"$script_directory/build-binary.sh" "$temporary_directory/switchyard"
SWITCHYARD_TEST_HELPER="$temporary_directory/switchyard" swift test --package-path app
swift build --package-path app -c release --product SwitchyardApp
"$temporary_directory/switchyard" version >/dev/null
