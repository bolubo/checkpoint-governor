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

# Building is not enough: a changed signature also breaks the tests that call it, and those errors appear only
# when the test files compile. `go vet` does that, and it is what CI runs first.
if ! out=$(go vet ./... 2>&1); then
	printf '%s\n' "$out" | sed 's/^/  /' >&2
	printf 'the PR tree does not vet — a changed signature and its callers are split across the file list\n' >&2
	exit 1
fi
printf 'the PR tree vets\n'

# A Makefile is a build input too. The PR that shipped this lesson had a Makefile missing `fast`, and `go vet`
# said the tree was fine — CI then failed in sixteen seconds with "No rule to make target 'fast'".
if ! out=$(make -n fast 2>&1); then
	printf '%s\n' "$out" | sed 's/^/  /' >&2
	printf 'the PR tree has no usable `fast` target\n' >&2
	exit 1
fi
printf 'the PR tree has a usable fast target\n'

# And the tests, run rather than compiled. `go vet` opened the test files and was satisfied; the assertion inside
# one of them failed on CI because the change it tests was in a file the list did not include. Compiling a test and
# running it are different questions, and only one of them is the one that matters.
if ! out=$(go test ./... 2>&1); then
	printf '%s\n' "$out" | grep -E '^(---|FAIL|ok)' | sed 's/^/  /' >&2
	printf 'the PR tree does not pass its own tests\n' >&2
	exit 1
fi
printf 'the PR tree passes its tests\n'
