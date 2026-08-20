#!/bin/sh
# Plans, and only on explicit request applies, a rollback of the published
# release pointer. Rollback never rewrites or deletes a tag, release, or asset:
# it re-points "latest" (which SUFeedURL and the README download link follow)
# at a previously published good version and tells you how to revert the tap.
#
# usage: release-rollback.sh <good-version>            # print the plan only
#        release-rollback.sh <good-version> --apply    # re-point latest with gh
#
# Sparkle never downgrades an installed app, so a rollback protects new installs
# and stops further upgrades; existing installs of the bad version need a
# fix-forward release with a higher version number.
set -eu

repository="theronburger/switchyard"
tap_repository="theronburger/homebrew-tap"

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
	echo "usage: $0 <good-version> [--apply]" >&2
	exit 2
fi
good_version=${1#v}
mode=${2:-plan}
case "$good_version" in
	[0-9]*.[0-9]*.[0-9]*) ;;
	*) echo "good version must be a semantic version: $good_version" >&2; exit 2 ;;
esac
case "$mode" in
	plan|--apply) ;;
	*) echo "unknown mode: $mode" >&2; exit 2 ;;
esac
tag="v$good_version"
archive="switchyard_${good_version}_macos_universal.zip"

cat <<PLAN
Rollback plan for $repository

1. Re-point the GitHub "latest" release at $tag.
   SUFeedURL resolves https://github.com/$repository/releases/latest/download/appcast.xml,
   so every installed app immediately sees the $good_version appcast and offers no update.
     gh release edit $tag --repo $repository --latest --draft=false --prerelease=false

2. Verify the rolled-back pointer serves the expected signed appcast and archive.
     curl -fsSL https://github.com/$repository/releases/latest/download/appcast.xml \\
       | scripts/verify-appcast.sh /dev/stdin $good_version $archive

3. Revert the Homebrew tap to the $good_version Cask. The release workflow's
   downgrade guard refuses to do this automatically; make the downgrade explicit:
     git clone git@github.com:$tap_repository.git
     git -C homebrew-tap log --oneline -- Casks/switchyard.rb
     git -C homebrew-tap revert --no-edit <commit that updated Switchyard past $good_version>
     git -C homebrew-tap push origin HEAD:main

4. Keep the bad release visible. Mark it as a pre-release and add a note to its
   body instead of deleting it, so provenance, checksums, and the SBOM remain
   auditable and the history-rewrite rules in docs/RELEASING.md are not violated.
     gh release edit <bad tag> --repo $repository --prerelease --notes-file <note>

5. Fix forward. Installed copies of the bad version update only to a higher
   version, so land the fix with conventional commits and let Release Please cut
   the next version.

6. Local recovery for an affected machine, scoped to the Switchyard app only:
     brew reinstall --cask switchyard
     xattr -dr com.apple.quarantine "/Applications/Switchyard.app"
     open -a "Switchyard"
   The app reinstalls its bundled daemon by content digest, so an older app
   restores the matching older daemon on next launch.
PLAN

if [ "$mode" != "--apply" ]; then
	exit 0
fi

command -v gh >/dev/null 2>&1 || { echo "gh is required for --apply" >&2; exit 1; }
if [ "$(gh release view "$tag" --repo "$repository" --json isDraft --jq '.isDraft')" != "false" ]; then
	echo "refusing: $tag is a draft or could not be inspected" >&2
	exit 1
fi
gh release view "$tag" --repo "$repository" --json assets --jq '.assets[].name' | grep -Fxq "appcast.xml" \
	|| { echo "refusing: $tag has no appcast.xml asset" >&2; exit 1; }
gh release view "$tag" --repo "$repository" --json assets --jq '.assets[].name' | grep -Fxq "$archive" \
	|| { echo "refusing: $tag has no $archive asset" >&2; exit 1; }
gh release edit "$tag" --repo "$repository" --latest --draft=false --prerelease=false
echo "latest now points at $tag; complete steps 2 to 5 manually."
