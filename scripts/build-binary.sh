#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: $0 <output-path>" >&2
	exit 2
fi

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
output_path=$1
release_version=$(tr -d '[:space:]' < "$repository_root/VERSION")
deployment_target=${MACOSX_DEPLOYMENT_TARGET:-15.0}
build_channel=${SWITCHYARD_BUILD_CHANNEL:-development}

case "$build_channel" in
	development|release) ;;
	*)
		echo "unsupported Switchyard build channel: $build_channel" >&2
		exit 2
		;;
esac

mkdir -p "$(dirname -- "$output_path")"
(
	cd "$repository_root"
	CGO_ENABLED=1 \
		CGO_CFLAGS="-mmacosx-version-min=$deployment_target" \
		CGO_LDFLAGS="-mmacosx-version-min=$deployment_target" \
		MACOSX_DEPLOYMENT_TARGET="$deployment_target" \
		go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$release_version -X main.buildChannel=$build_channel" -o "$output_path" ./cmd/switchyard
)
