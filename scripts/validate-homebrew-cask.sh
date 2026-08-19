#!/bin/sh
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
version=$(tr -d '[:space:]' < "$repository_root/VERSION")
audit_tap="switchyard/release-audit"

if brew tap | grep -Fxq "$audit_tap"; then
	echo "temporary Homebrew audit tap already exists: $audit_tap" >&2
	exit 1
fi

brew tap-new --no-git "$audit_tap" >/dev/null
cleanup() {
	brew untap "$audit_tap" >/dev/null
}
trap cleanup EXIT HUP INT TERM

tap_directory=$(brew --repository "$audit_tap")
mkdir -p "$tap_directory/Casks"
cask_path="$tap_directory/Casks/switchyard.rb"
"$script_directory/render-homebrew-cask.sh" \
	"$version" \
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" \
	> "$cask_path"

ruby -c "$cask_path"
brew install --cask --dry-run "$audit_tap/switchyard"
