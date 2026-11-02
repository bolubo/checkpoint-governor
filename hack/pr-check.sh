#!/bin/sh
# pr-check — build the tree the PR will actually be, before it is a PR.
#
# The failure this exists for: a field was declared in one file and used in another, both were on disk, `make
# ci` was green — and CI went red, because the PR's file list contained the user but not the declaration. CI
# compiles *main plus this PR's files*, which is a smaller tree than the working directory.
#
#   sh hack/pr-check.sh internal/loop/loop.go internal/advise/status.go
#
# Everything not named comes from origin/main. What fails here would fail there, one minute earlier.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if [ "$#" -eq 0 ]; then
	printf 'usage: sh hack/pr-check.sh <file>...   (files this PR changes)\n' >&2
	exit 2
fi

git fetch --quiet origin main
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

git archive origin/main | tar -x -C "$work"

missing=0
for f in "$@"; do
	if [ ! -f "$f" ]; then
		printf 'not in the working tree: %s\n' "$f" >&2
		missing=1
		continue
	fi
	mkdir -p "$work/$(dirname "$f")"
	cp "$f" "$work/$f"
done
[ "$missing" -eq 0 ] || exit 2

printf 'building the PR tree (%d file(s) over origin/main)\n' "$#"
cd "$work"
# The status must come from go, not from sed: through a pipe, `$?` is the last command's, and this script would
# report success on a tree that does not compile — the same mistake it exists to catch.
if ! out=$(go build ./... 2>&1); then
	printf '%s\n' "$out" | sed 's/^/  /' >&2
	printf 'the PR tree does not build — a declaration and its use are split across the file list\n' >&2
	exit 1
fi
printf 'the PR tree builds\n'
