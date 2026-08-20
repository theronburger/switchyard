#!/bin/sh
# Renders the Cask template for the current VERSION into a throwaway local tap
# and asks the installed Homebrew to parse and dry-run it. Homebrew 6 requires
# non-official casks to be trusted; the temporary trust entry is removed again
# unless it already existed before this run.
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(dirname -- "$script_directory")
version=$(tr -d '[:space:]' < "$repository_root/VERSION")
audit_tap="switchyard/release-audit"
audit_cask="$audit_tap/switchyard"
export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_ENV_HINTS=1 HOMEBREW_NO_INSTALL_CLEANUP=1

if brew tap | grep -Fxq "$audit_tap"; then
	echo "temporary Homebrew audit tap already exists: $audit_tap" >&2
	exit 1
fi

previously_trusted=0
if brew trust --cask --json=v1 2>/dev/null | grep -Fq "\"$audit_cask\""; then
	previously_trusted=1
fi

brew tap-new --no-git "$audit_tap" >/dev/null
cleanup() {
	brew untap "$audit_tap" >/dev/null 2>&1 || true
	if [ "$previously_trusted" -eq 0 ]; then
		brew untrust --cask "$audit_cask" >/dev/null 2>&1 || true
	fi
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
brew trust --cask "$audit_cask" >/dev/null
brew install --cask --dry-run "$audit_cask"
if [ "${SWITCHYARD_SKIP_BREW_STYLE:-}" != "1" ]; then
	brew style "$cask_path"
fi
