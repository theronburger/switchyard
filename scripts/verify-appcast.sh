#!/bin/sh
# Verifies a generated Sparkle appcast against the release it must describe.
#
# usage: verify-appcast.sh <appcast.xml> <version> <archive-file-name> [minimum-system-version]
#                          [--archive <path>] [--public-key <base64-ed25519-key>]
#        verify-appcast.sh --self-test
#
# Checks that exactly one item is published, that it advertises the exact
# release version, that its enclosure points at the tagged GitHub release asset,
# that it carries an EdDSA signature and a numeric length, and that its minimum
# system version matches the app's LSMinimumSystemVersion.
#
# When --archive and --public-key are both supplied the script additionally
# proves the enclosure length equals the archive size and that the
# sparkle:edSignature is a valid Ed25519 signature over the archive bytes for
# that public key (the app's SUPublicEDKey). The release workflow always passes
# both, so an appcast signed with the wrong seed or describing a different
# archive can never be armed as "latest".
set -eu

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
signature_verifier="$script_directory/verify-sparkle-signature.swift"

xpath() {
	xmllint --xpath "$1" "$2" 2>/dev/null
}

file_size() {
	stat -f '%z' "$1" 2>/dev/null || stat -c '%s' "$1"
}

# verify <appcast> <version> <archive-file-name> [minimum-system] [archive-path] [public-key]
verify() {
	appcast=$1
	version=$2
	archive=$3
	minimum_system=${4:-}
	archive_path=${5:-}
	public_key=${6:-}
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
	if [ -n "$archive_path" ] || [ -n "$public_key" ]; then
		test -n "$archive_path" && test -n "$public_key" || { echo "--archive and --public-key must be supplied together" >&2; return 1; }
		test -f "$archive_path" || { echo "archive not found: $archive_path" >&2; return 1; }
		actual_length=$(file_size "$archive_path")
		test "$length" = "$actual_length" || { echo "appcast enclosure length is $length, archive is $actual_length bytes" >&2; return 1; }
		swift "$signature_verifier" "$archive_path" "$public_key" "$signature" >/dev/null \
			|| { echo "appcast sparkle:edSignature does not verify against the app's public key" >&2; return 1; }
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
      <enclosure url="$3" length="${5:-123456}" type="application/octet-stream" sparkle:edSignature="$4"/>
    </item>
  </channel>
</rss>
XML
}

# Generates an ephemeral Ed25519 key, signs the archive, and prints
# "<base64-public-key> <base64-signature>" for the self-test.
sign_fixture() {
	signer="$(dirname -- "$1")/sign-fixture.swift"
	cat > "$signer" <<'SWIFT'
import CryptoKit
import Foundation
let archive = FileManager.default.contents(atPath: CommandLine.arguments[1])!
let key = Curve25519.Signing.PrivateKey()
let signature = try! key.signature(for: archive)
print(key.publicKey.rawRepresentation.base64EncodedString(), signature.base64EncodedString())
SWIFT
	swift "$signer" "$1"
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

	# Cryptographic signature checks against a real Ed25519 key.
	printf 'synthetic archive bytes\n' > "$directory/$archive"
	size=$(file_size "$directory/$archive")
	signed=$(sign_fixture "$directory/$archive")
	public_key=${signed% *}
	signature=${signed#* }
	other=$(sign_fixture "$directory/$archive")
	other_public_key=${other% *}

	write_fixture "$directory/signed.xml" 0.2.0 "$good_url" "$signature" "$size"
	verify "$directory/signed.xml" 0.2.0 "$archive" 15.0 "$directory/$archive" "$public_key" \
		|| { echo "correctly signed appcast must pass" >&2; exit 1; }
	if verify "$directory/signed.xml" 0.2.0 "$archive" 15.0 "$directory/$archive" "$other_public_key" 2>/dev/null; then
		echo "signature from another key must fail" >&2; exit 1
	fi
	write_fixture "$directory/placeholder.xml" 0.2.0 "$good_url" "c2lnbmF0dXJl" "$size"
	if verify "$directory/placeholder.xml" 0.2.0 "$archive" 15.0 "$directory/$archive" "$public_key" 2>/dev/null; then
		echo "malformed signature must fail" >&2; exit 1
	fi
	write_fixture "$directory/length.xml" 0.2.0 "$good_url" "$signature" "$((size + 1))"
	if verify "$directory/length.xml" 0.2.0 "$archive" 15.0 "$directory/$archive" "$public_key" 2>/dev/null; then
		echo "length mismatch must fail" >&2; exit 1
	fi
	printf 'tampered\n' >> "$directory/$archive"
	if verify "$directory/signed.xml" 0.2.0 "$archive" 15.0 "$directory/$archive" "$public_key" 2>/dev/null; then
		echo "tampered archive must fail" >&2; exit 1
	fi
	if verify "$directory/signed.xml" 0.2.0 "$archive" 15.0 "$directory/$archive" "" 2>/dev/null; then
		echo "archive without public key must fail" >&2; exit 1
	fi
	echo "verify-appcast self-test passed"
}

if [ "${1:-}" = "--self-test" ]; then
	self_test
	exit 0
fi

positional_count=0
appcast_argument=""
version_argument=""
archive_name_argument=""
minimum_system_argument=""
archive_path_argument=""
public_key_argument=""
while [ "$#" -gt 0 ]; do
	case "$1" in
		--archive)
			shift
			test "$#" -gt 0 || { echo "--archive requires a path" >&2; exit 2; }
			archive_path_argument=$1
			;;
		--public-key)
			shift
			test "$#" -gt 0 || { echo "--public-key requires a base64 key" >&2; exit 2; }
			public_key_argument=$1
			;;
		--*)
			echo "unknown option: $1" >&2
			exit 2
			;;
		*)
			positional_count=$((positional_count + 1))
			case "$positional_count" in
				1) appcast_argument=$1 ;;
				2) version_argument=$1 ;;
				3) archive_name_argument=$1 ;;
				4) minimum_system_argument=$1 ;;
				*) echo "too many arguments" >&2; exit 2 ;;
			esac
			;;
	esac
	shift
done
if [ "$positional_count" -lt 3 ]; then
	echo "usage: $0 <appcast.xml> <version> <archive-file-name> [minimum-system-version] [--archive <path>] [--public-key <key>] | --self-test" >&2
	exit 2
fi
verify "$appcast_argument" "$version_argument" "$archive_name_argument" "$minimum_system_argument" "$archive_path_argument" "$public_key_argument"
