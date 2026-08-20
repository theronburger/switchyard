#!/bin/sh
set -eu

title_pattern='^(feat|fix|docs|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9][a-z0-9._/-]*\))?!?: .+'

check_title() {
	printf '%s\n' "$1" | grep -Eq "$title_pattern"
}

run_self_test() {
	for title in \
		'feat: add private profiles' \
		'fix(cleanup): preserve foreign resources' \
		'feat!: replace the daemon contract' \
		'chore(main): release switchyard 0.2.0' \
		'chore(deps): update Go dependencies'
	do
		check_title "$title"
	done

	for title in \
		'Add private profiles' \
		'feature: add private profiles' \
		'feat: ' \
		'feat(Settings): use lowercase scopes'
	do
		if check_title "$title"; then
			echo "Invalid PR title accepted: $title" >&2
			exit 1
		fi
	done
}

if [ "${1:-}" = "--self-test" ]; then
	run_self_test
	exit 0
fi

if [ "$#" -ne 1 ]; then
	echo "usage: $0 <pull request title>" >&2
	exit 2
fi

if ! check_title "$1"; then
	echo "Pull request titles must use Conventional Commits." >&2
	echo "Expected: type: summary or type(scope): summary" >&2
	echo "Allowed types: feat, fix, docs, refactor, perf, test, build, ci, chore, revert" >&2
	exit 1
fi
