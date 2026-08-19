#!/bin/sh
set -eu

if [ "$#" -ne 2 ]; then
	echo "usage: render-homebrew-cask.sh <version> <sha256>" >&2
	exit 2
fi

script_directory=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
template="$script_directory/../packaging/homebrew/switchyard.rb.template"

sed \
	-e "s/@VERSION@/$1/g" \
	-e "s/@SHA256@/$2/g" \
	"$template"
