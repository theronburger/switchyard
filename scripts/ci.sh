#!/bin/sh
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/switchyard-ci.XXXXXX")
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM

cd "$repository_root"
"$script_directory/release-checks.sh"
unformatted_files=$(gofmt -l $(find cmd internal -name '*.go' -type f))
if [ -n "$unformatted_files" ]; then
	echo "gofmt required for:" >&2
	echo "$unformatted_files" >&2
	exit 1
fi
go mod tidy -diff
go vet ./...
go test -race ./...
swift test --package-path app
swift run --package-path app SwitchyardContractCheck contracts/v1/fixtures/status.json
swift build --package-path app -c release --product SwitchyardApp
"$script_directory/build-binary.sh" "$temporary_directory/switchyard"
"$temporary_directory/switchyard" version >/dev/null
