#!/bin/sh
# The gate a push must pass, in the same order CI runs it.
#
# This exists because of a specific failure that repeated for hours: `make verify` runs two of the four
# steps CI runs, so a green local run kept meeting a red CI. The gate a push passes has to be the gate it
# will meet, or the green light means nothing.
#
# Use it as the last command before opening a PR:
#
#     ./hack/prepush.sh && <open the PR>
#
# It prints the failing step and stops there, so the reason is on screen instead of in a log.
set -eu

cd "$(dirname "$0")/.."

step() {
	printf '== %s\n' "$1"
	shift
	if ! "$@"; then
		printf '\nFAILED at: %s\n' "$*"
		printf 'A push now would go red in CI. Fix it here first.\n'
		exit 1
	fi
}

step "make fast" make fast
step "make gate" make gate
step "make e2e" make e2e
step "make exp" make exp
step "make demo" make demo
step "make install-smoke" make install-smoke
step "make spot" make spot
step "make resume" make resume
step "make py" make py
step "make drill" make drill

printf '\nall ten steps of the CI gate passed: this push should not go red\n'
