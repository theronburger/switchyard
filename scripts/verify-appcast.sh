#!/bin/sh
# Verifies a generated Sparkle appcast against the release it must describe.
#
# usage: verify-appcast.sh <appcast.xml> <version> <archive-file-name> [minimum-system-version]
#        verify-appcast.sh --self-test
#
# Checks that exactly one item is published, that it advertises the exact
# release version, that its enclosure points at the tagged GitHub release asset,
# that it carries an EdDSA signature and length, and that its minimum system
# version matches the app's LSMinimumSystemVersion. Signature validity itself is
# proven by Sparkle during the signed-launch and update path; this guards the
# metadata an attacker-free but misconfigured pipeline could still get wrong.
set -eu

xpath() {
	xmllint --xpath "$1" "$2" 2>/dev/null
}

verify() {
	appcast=$1
	version=$2
	archive=$3
	minimum_system=${4:-}
	expected_url="https://github.com/theronburger/switchyard/releases/download/v$version/$archive"

	xmllint --noout "$appcast"
	item_count=$(xpath 'count(/rss/channel/item)' "$appcast")
	test "$item_count" = "1" || { echo "appcast must contain exactly one item, found $item_count" >&2; return 1; }
	actual_version=$(xpath 'string(/rss/channel/item/*[local-name()="version"])' "$appcast")
	test "$actual_version" = "$version" || { echo "appcast sparkle:version is '$actual_version', expected $version" >&2; return 1; }
	short_version=$(xpath 'string(/rss/channel/item/*[local-name()="shortVersionString"])' "$appcast")
	test -z "$short_version" || test "$short_version" = "$version" || { echo "appcast sparkle:shortVersionString is '$short_version', expected $version" >&2; return 1; }
	actual_url=$(xpath 'string(/rss/channel/item/enclosure/@url)' "$appcast")
	test "$actual_url" = "$expected_url" || { echo "appcast enclosure url is '$actual_url', expected $expected_url" >&2; return 1; }
	signature=$(xpath 'string(/rss/channel/item/enclosure/@*[local-name()="edSignature"])' "$appcast")
	test -n "$signature" || { echo "appcast enclosure has no sparkle:edSignature" >&2; return 1; }
	length=$(xpath 'string(/rss/channel/item/enclosure/@length)' "$appcast")
	case "$length" in
		''|*[!0-9]*) echo "appcast enclosure length is not numeric: '$length'" >&2; return 1 ;;
	esac
	if [ -n "$minimum_system" ]; then
		actual_minimum=$(xpath 'string(/rss/channel/item/*[local-name()="minimumSystemVersion"])' "$appcast")
		test "$actual_minimum" = "$minimum_system" || { echo "appcast minimumSystemVersion is '$actual_minimum', expected $minimum_system" >&2; return 1; }
	fi
}

write_fixture() {
	cat > "$1" <<XML
<?xml version="1.0" encoding="utf-8"?>
<rss version="2.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle">
  <channel>
    <title>Switchyard</title>
    <item>
      <title>$2</title>
      <sparkle:version>$2</sparkle:version>
      <sparkle:shortVersionString>$2</sparkle:shortVersionString>
      <sparkle:minimumSystemVersion>15.0</sparkle:minimumSystemVersion>
      <enclosure url="$3" length="123456" type="application/octet-stream" sparkle:edSignature="$4"/>
    </item>
  </channel>
</rss>
XML
}

self_test() {
	directory=$(mktemp -d "${TMPDIR:-/tmp}/switchyard-appcast.XXXXXX")
	trap 'rm -rf "$directory"' EXIT HUP INT TERM
	archive=switchyard_0.2.0_macos_universal.zip
	good_url="https://github.com/theronburger/switchyard/releases/download/v0.2.0/$archive"

	write_fixture "$directory/good.xml" 0.2.0 "$good_url" "c2lnbmF0dXJl"
	verify "$directory/good.xml" 0.2.0 "$archive" 15.0 || { echo "valid appcast must pass" >&2; exit 1; }

	write_fixture "$directory/version.xml" 0.1.0 "$good_url" "c2lnbmF0dXJl"
	if verify "$directory/version.xml" 0.2.0 "$archive" 2>/dev/null; then echo "wrong version must fail" >&2; exit 1; fi

	write_fixture "$directory/url.xml" 0.2.0 "https://example.invalid/$archive" "c2lnbmF0dXJl"
	if verify "$directory/url.xml" 0.2.0 "$archive" 2>/dev/null; then echo "foreign enclosure url must fail" >&2; exit 1; fi

	write_fixture "$directory/unsigned.xml" 0.2.0 "$good_url" ""
	if verify "$directory/unsigned.xml" 0.2.0 "$archive" 2>/dev/null; then echo "unsigned enclosure must fail" >&2; exit 1; fi

	if verify "$directory/good.xml" 0.2.0 "$archive" 14.0 2>/dev/null; then echo "minimum system mismatch must fail" >&2; exit 1; fi

	sed 's|</channel>|<item><sparkle:version>0.1.0</sparkle:version><enclosure url="x" length="1"/></item></channel>|' "$directory/good.xml" > "$directory/two.xml"
	if verify "$directory/two.xml" 0.2.0 "$archive" 2>/dev/null; then echo "multiple items must fail" >&2; exit 1; fi
	echo "verify-appcast self-test passed"
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit 0
fi
if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
	echo "usage: $0 <appcast.xml> <version> <archive-file-name> [minimum-system-version] | --self-test" >&2
	exit 2
fi
verify "$@"
