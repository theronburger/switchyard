#!/bin/bash
set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository_root="$(cd "$script_directory/.." && pwd)"
source_png="$repository_root/packaging/SwitchyardIcon-Source.png"
menu_png="$repository_root/packaging/SwitchyardMenuIcon.png"
iconset="$(mktemp -d "${TMPDIR:-/tmp}/switchyard-icon.XXXXXX")/Switchyard.iconset"
mkdir -p "$iconset"
install -m 0644 \
    "$repository_root/packaging/SwitchyardTile.png" \
    "$repository_root/app/Sources/SwitchyardApp/Resources/SwitchyardTile.png"

swift "$script_directory/generate-icon.swift" \
    "$repository_root/packaging/SwitchyardTile.png" \
    "$source_png" \
    "$menu_png"
for specification in \
    "16 icon_16x16.png" \
    "32 icon_16x16@2x.png" \
    "32 icon_32x32.png" \
    "64 icon_32x32@2x.png" \
    "128 icon_128x128.png" \
    "256 icon_128x128@2x.png" \
    "256 icon_256x256.png" \
    "512 icon_256x256@2x.png" \
    "512 icon_512x512.png" \
    "1024 icon_512x512@2x.png"; do
    read -r size filename <<<"$specification"
    sips -z "$size" "$size" "$source_png" --out "$iconset/$filename" >/dev/null
done
iconutil -c icns "$iconset" -o "$repository_root/packaging/Switchyard.icns"
rm -rf "$(dirname "$iconset")"
