#!/bin/bash
set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(cd "$script_directory/.." && pwd)"
distribution_directory="$repository_root/dist"
staging_directory="$(mktemp -d "${TMPDIR:-/tmp}/switchyard-bundle.XXXXXX")"
app_bundle="$staging_directory/Switchyard.app"

swift build --package-path "$repository_root/app" -c release --product SwitchyardApp
swift_binary_directory="$(swift build --package-path "$repository_root/app" -c release --show-bin-path)"

mkdir -p "$app_bundle/Contents/MacOS" "$app_bundle/Contents/Resources"
install -m 0755 "$swift_binary_directory/SwitchyardApp" "$app_bundle/Contents/MacOS/SwitchyardApp"
install -m 0644 "$repository_root/packaging/Switchyard-Info.plist" "$app_bundle/Contents/Info.plist"
go build -trimpath -ldflags "-s -w" -o "$app_bundle/Contents/Resources/SwitchyardDaemon" "$repository_root/cmd/switchyard"
codesign --force --deep --sign - "$app_bundle"

mkdir -p "$distribution_directory"
if [[ -e "$distribution_directory/Switchyard.app" ]]; then
    previous_bundle="$HOME/.Trash/Switchyard-previous-$(uuidgen).app"
    mv "$distribution_directory/Switchyard.app" "$previous_bundle"
fi
mv "$app_bundle" "$distribution_directory/Switchyard.app"
rmdir "$staging_directory"

echo "$distribution_directory/Switchyard.app"
