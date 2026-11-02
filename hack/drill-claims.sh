#!/bin/sh
# The drill's claims: a one-step loader earns `restorable`, a step-0 loader does not, a loader that changes
# the checkpoint is refused, and a missing loader is a refusal rather than an invention.
#
#   sh hack/drill-claims.sh
#
# The loader CI uses is a synthetic Go program (it reads the manifest and reports the next step), so the
# machinery — fingerprint, environment, timeout, step judgement — runs on every push without torch.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
GO=${GO:-go}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT INT TERM

"$GO" build -o "$TMP/ckptctl" "$ROOT/cmd/ckptctl"
# The loader carries a build tag so `go build ./...` skips it; the CI gate builds it explicitly.
"$GO" build -o "$TMP/drill-loader" "$ROOT/experiments/rig/tools/drill-loader/main.go"

pass=0
fail=0
claim() { # claim <name> <want-exit> <want-text> <args...>
	name=$1
	want_code=$2
	want=$3
	shift 3
	got=$("$TMP/ckptctl" "$@" 2>&1) && code=0 || code=$?
	if [ "$code" != "$want_code" ]; then
		printf 'FAIL %-16s exit %s, want %s: %s\n' "$name" "$code" "$want_code" "$got"
		fail=$((fail + 1))
		return 0
	fi
	case "$got" in
	*"$want"*) printf 'ok   %-16s %s\n' "$name" "$got" ;;
	*)
		printf 'FAIL %-16s %q does not contain %q\n' "$name" "$got" "$want"
		fail=$((fail + 1))
		;;
	esac
}

# A fresh copy per claim: the mutating claim must not touch the tree the next claim inspects.
fresh() { rm -rf "$TMP/job"; cp -R "$ROOT/internal/resume/testdata/full" "$TMP/job"; }

fresh
claim restorable 0 "resume: ok — step 512 → 513" drill --dir="$TMP/job" --loader="$TMP/drill-loader"

fresh
claim wrong-step 1 "not restorable: the loader reported step 0 (declared 512)" \
	drill --dir="$TMP/job" --loader="echo step=0"

fresh
claim mutation-refused 2 "drill refused: the checkpoint changed during the drill" \
	drill --dir="$TMP/job" --loader='printf x >> "$CKPT_DIR/rank-00.bin"; echo step=513'

fresh
claim no-loader 2 "drill refused: no loader given" drill --dir="$TMP/job"

if [ "$fail" -ne 0 ]; then
	printf '\ndrill: %s failed\n' "$fail"
	exit 1
fi
printf '\ndrill: 4/4 — restorable, wrong-step, mutation-refused, no-loader\n'
