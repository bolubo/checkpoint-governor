#!/bin/sh
# The resume claims, asserted against the checked-in fixtures: one command, six sentences, one tally.
#
#   sh hack/resume-claims.sh
#
# Each line is a fact a person can act on before a preemption (`resumable`) or after a restart
# (`adopted`), and the exit code is part of the claim: 0 yes, 1 checked and no, 2 cannot decide.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
GO=${GO:-go}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

"$GO" build -o "$TMP/ckptctl" "$ROOT/cmd/ckptctl"
FIX="$ROOT/internal/resume/testdata"

pass=0
fail=0
claim() { # claim <name> <want-exit> <want-text> <args...>
	name=$1
	want_code=$2
	want=$3
	shift 3
	got=$("$TMP/ckptctl" "$@" 2>&1) && code=0 || code=$?
	if [ "$code" != "$want_code" ]; then
		printf 'FAIL %-14s exit %s, want %s: %s\n' "$name" "$code" "$want_code" "$got"
		fail=$((fail + 1))
		return 0
	fi
	case "$got" in
	*"$want"*) printf 'ok   %-14s %s\n' "$name" "$got" ;;
	*)
		printf 'FAIL %-14s %q does not contain %q\n' "$name" "$got" "$want"
		fail=$((fail + 1))
		;;
	esac
}

claim resumable 0 "resumable (gen 512, step 512, world 8/8, full state)" resumable --dir="$FIX/full"
claim weights-only 1 "not resumable: optimizer state absent" resumable --dir="$FIX/weights-only"
claim world-changed 1 "not resumable: world changed 8 → 16" resumable --dir="$FIX/full" --world=16
claim reshardable 0 "resumable with resharding (8 → 16 ranks)" resumable --dir="$FIX/reshardable" --world=16
claim unstated 2 "unknown: sealed, but nobody said what it holds" resumable --dir="$FIX/no-resume"

# Adoption is about the job's own report, so the copy is a fixture plus the glue a resumed job leaves.
cp -R "$FIX/full" "$TMP/adopted"
mkdir -p "$TMP/adopted/.ckpt-governor"
printf 512 >"$TMP/adopted/.ckpt-governor/resumed-generation"
printf 512 >"$TMP/adopted/.ckpt-governor/resumed-step"
printf 9.1 >"$TMP/adopted/.ckpt-governor/load-seconds"
claim adopted 0 "adopted gen 512 (step 512), load 9.1s" resumed --dir="$TMP/adopted"

if [ "$fail" -ne 0 ]; then
	printf '\nresume: %s failed\n' "$fail"
	exit 1
fi
printf '\nresume: 6/6 — resumable, weights-only, world-changed, resharding, unstated, adoption\n'
