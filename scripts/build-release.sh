#!/bin/sh
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
output_directory=${1:-"$repository_root/dist"}
release_version=$(tr -d '[:space:]' < "$repository_root/VERSION")
release_stem="switchyard_${release_version}_macos_universal"
export SWITCHYARD_BUILD_CHANNEL=release

mkdir -p "$output_directory"
output_directory=$(CDPATH= cd -- "$output_directory" && pwd)
staging_directory=$(mktemp -d "$output_directory/.switchyard-release.XXXXXX")
trap 'rm -rf "$staging_directory"' EXIT HUP INT TERM

application_path="$staging_directory/Switchyard.app"
archive_path="$staging_directory/$release_stem.zip"
sbom_path="$staging_directory/$release_stem.sbom.cdx.json"

mkdir -p "$staging_directory/bin"
GOOS=darwin GOARCH=arm64 "$script_directory/build-binary.sh" "$staging_directory/bin/switchyard-arm64"
GOOS=darwin GOARCH=amd64 "$script_directory/build-binary.sh" "$staging_directory/bin/switchyard-amd64"
lipo -create \
	"$staging_directory/bin/switchyard-arm64" \
	"$staging_directory/bin/switchyard-amd64" \
	-output "$staging_directory/bin/switchyard-universal"

"$script_directory/build-app-bundle.sh" "$application_path" "$staging_directory/bin/switchyard-universal"
ditto -c -k --sequesterRsrc --keepParent "$application_path" "$archive_path"

sbom_generator=$(command -v cyclonedx-gomod || true)
if [ -z "$sbom_generator" ]; then
	go_binary_directory=$(go env GOPATH)/bin
	if [ -x "$go_binary_directory/cyclonedx-gomod" ]; then
		sbom_generator="$go_binary_directory/cyclonedx-gomod"
	fi
fi
if [ -z "$sbom_generator" ]; then
	echo "cyclonedx-gomod v1.10.0 is required for release packaging" >&2
	exit 1
fi
(
	cd "$repository_root"
	"$sbom_generator" app -json -output "$sbom_path" -main ./cmd/switchyard
)

(
	cd "$staging_directory"
	shasum -a 256 "$(basename -- "$archive_path")" "$(basename -- "$sbom_path")" > checksums.txt
)

lipo "$application_path/Contents/MacOS/SwitchyardApp" -verify_arch arm64 x86_64
lipo "$application_path/Contents/Resources/SwitchyardDaemon" -verify_arch arm64 x86_64
codesign --verify --deep --strict --verbose=2 "$application_path"
"$staging_directory/bin/switchyard-universal" version >/dev/null

rm -rf "$output_directory/Switchyard.app"
rm -f "$output_directory/$release_stem.zip" "$output_directory/$release_stem.sbom.cdx.json" "$output_directory/checksums.txt"
mv "$application_path" "$output_directory/Switchyard.app"
mkdir -p "$output_directory/bin"
for binary in switchyard-arm64 switchyard-amd64 switchyard-universal; do
	mv -f "$staging_directory/bin/$binary" "$output_directory/bin/$binary"
done
mv "$archive_path" "$output_directory/$release_stem.zip"
mv "$sbom_path" "$output_directory/$release_stem.sbom.cdx.json"
mv "$staging_directory/checksums.txt" "$output_directory/checksums.txt"

echo "$output_directory/$release_stem.zip"
