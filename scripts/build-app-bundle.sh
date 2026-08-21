#!/bin/sh
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
default_destination="$repository_root/dist/Switchyard.app"
destination=${1:-"$default_destination"}
prebuilt_binary=${2:-}
release_version=$(tr -d '[:space:]' < "$repository_root/VERSION")
build_channel=${SWITCHYARD_BUILD_CHANNEL:-development}

case "$build_channel" in
	development|release) ;;
	*)
		echo "unsupported Switchyard build channel: $build_channel" >&2
		exit 2
		;;
esac

if [ -e "$destination" ]; then
	if [ "$destination" != "$default_destination" ]; then
		echo "Destination already exists: $destination" >&2
		exit 1
	fi
	mkdir -p "$HOME/.Trash"
	mv "$destination" "$HOME/.Trash/Switchyard-previous-$(uuidgen).app"
fi

contents="$destination/Contents"
mkdir -p "$contents/MacOS" "$contents/Resources" "$contents/Frameworks"
cp "$repository_root/packaging/Switchyard-Info.plist" "$contents/Info.plist"
cp "$repository_root/packaging/Switchyard.icns" "$contents/Resources/Switchyard.icns"
cp "$repository_root/packaging/SwitchyardMenuIcon.png" "$contents/Resources/SwitchyardMenuIcon.png"
cp "$repository_root/THIRD_PARTY_NOTICES.md" "$contents/Resources/THIRD_PARTY_NOTICES.md"
mkdir -p "$contents/Resources/skills"
cp -R "$repository_root/skills/switchyard" "$contents/Resources/skills/switchyard"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString $release_version" "$contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion $release_version" "$contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :SwitchyardChannel $build_channel" "$contents/Info.plist"
if [ "$build_channel" = "development" ]; then
	/usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier com.theronburger.switchyard.development" "$contents/Info.plist"
	/usr/libexec/PlistBuddy -c "Set :CFBundleDisplayName Switchyard Development" "$contents/Info.plist"
	/usr/libexec/PlistBuddy -c "Set :CFBundleName Switchyard Development" "$contents/Info.plist"
	/usr/libexec/PlistBuddy -c "Set :SUEnableAutomaticChecks false" "$contents/Info.plist"
	/usr/libexec/PlistBuddy -c "Set :SUAutomaticallyUpdate false" "$contents/Info.plist"
fi

swift_architecture_arguments=""
if [ -n "$prebuilt_binary" ]; then
	swift_architecture_arguments="--arch arm64 --arch x86_64"
fi
# shellcheck disable=SC2086
swift build --package-path "$repository_root/app" -c release --product SwitchyardApp $swift_architecture_arguments
# shellcheck disable=SC2086
swift_binary_directory=$(swift build --package-path "$repository_root/app" -c release --show-bin-path $swift_architecture_arguments)
cp "$swift_binary_directory/SwitchyardApp" "$contents/MacOS/SwitchyardApp"
chmod 0755 "$contents/MacOS/SwitchyardApp"
cp -R "$swift_binary_directory/Switchyard_SwitchyardApp.bundle" "$contents/Resources/Switchyard_SwitchyardApp.bundle"
ditto "$swift_binary_directory/Sparkle.framework" "$contents/Frameworks/Sparkle.framework"
install_name_tool -add_rpath "@executable_path/../Frameworks" "$contents/MacOS/SwitchyardApp"

if [ -n "$prebuilt_binary" ]; then
	cp "$prebuilt_binary" "$contents/Resources/SwitchyardDaemon"
else
	"$script_directory/build-binary.sh" "$contents/Resources/SwitchyardDaemon"
fi
chmod 0755 "$contents/Resources/SwitchyardDaemon"

signing_identity=${SWITCHYARD_SIGNING_IDENTITY:--}
if [ "$signing_identity" = "-" ]; then
	codesign --force --sign - --preserve-metadata=identifier,entitlements,flags "$contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Downloader.xpc"
	codesign --force --sign - --preserve-metadata=identifier,entitlements,flags "$contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Installer.xpc"
	codesign --force --sign - --preserve-metadata=identifier,entitlements,flags "$contents/Frameworks/Sparkle.framework/Versions/B/Updater.app"
	codesign --force --sign - "$contents/Frameworks/Sparkle.framework/Versions/B/Autoupdate"
	codesign --force --sign - --preserve-metadata=identifier,entitlements,flags "$contents/Frameworks/Sparkle.framework"
	codesign --force --sign - "$contents/Resources/SwitchyardDaemon"
	codesign --force --sign - --entitlements "$repository_root/packaging/Switchyard.entitlements" "$destination"
else
	codesign --force --options runtime --timestamp --sign "$signing_identity" --preserve-metadata=identifier,entitlements,flags "$contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Downloader.xpc"
	codesign --force --options runtime --timestamp --sign "$signing_identity" --preserve-metadata=identifier,entitlements,flags "$contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Installer.xpc"
	codesign --force --options runtime --timestamp --sign "$signing_identity" --preserve-metadata=identifier,entitlements,flags "$contents/Frameworks/Sparkle.framework/Versions/B/Updater.app"
	codesign --force --options runtime --timestamp --sign "$signing_identity" "$contents/Frameworks/Sparkle.framework/Versions/B/Autoupdate"
	codesign --force --options runtime --timestamp --sign "$signing_identity" --preserve-metadata=identifier,entitlements,flags "$contents/Frameworks/Sparkle.framework"
	codesign --force --options runtime --timestamp --sign "$signing_identity" "$contents/Resources/SwitchyardDaemon"
	codesign --force --options runtime --timestamp --sign "$signing_identity" --entitlements "$repository_root/packaging/Switchyard.entitlements" "$destination"
fi

codesign --verify --deep --strict --verbose=2 "$destination"
echo "$destination"
