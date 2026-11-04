#!/bin/sh
# Shared plumbing for the experiment rig. POSIX shell and kubectl only, because the rig has to run on
# the same machines the product runs on.
set -eu

# Every experiment gets one minute to say something, then it is killed and says where it was.
#
# A rig script that hangs is a bug; a rig script that hangs for ten minutes is a bug nobody runs
# again. The first run of anything gets the cap, so a mistake surfaces immediately — if it turns out
# the work really needs longer, BUDGET=300 is one word away. The child sets the flag, so the re-exec
# happens exactly once.
if [ -z "${CKPT_BUDGET_ARMED:-}" ]; then
	export CKPT_BUDGET_ARMED=1
	exec timeout --preserve-status "${BUDGET:-60}" sh "$0" "$@"
fi

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
RIG="$ROOT/experiments/rig"
RESULTS="$RIG/results.jsonl"
TMP=$(mktemp -d)
BIN="$TMP/bin"
mkdir -p "$BIN"

# A scenario that mounts a real volume registers it here, so an assertion that exits early still
# detaches the loop device before the temporary tree is removed. Both lists are space-separated.
DISK_MOUNTS=""
DISK_LOOPS=""

cleanup() {
	for m in $DISK_MOUNTS; do umount "$m" 2>/dev/null || true; done
	for l in $DISK_LOOPS; do losetup -d "$l" 2>/dev/null || true; done
	rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

# build caches the binaries under experiments/rig/.bin and rebuilds only when a source file is newer.
# Building on every experiment added a second of wall time to a measurement that takes milliseconds —
# and a rig that is slow is a rig nobody runs before a merge.
build() {
	mkdir -p "$RIG/.bin"
	BIN="$RIG/.bin"
	if [ -x "$BIN/ckptctl" ] && [ -x "$BIN/ckpttrainer" ] &&
		[ -z "$(find "$ROOT/cmd" "$ROOT/internal" -name '*.go' -newer "$BIN/ckptctl" -print -quit)" ]; then
		return 0
	fi
	go build -o "$BIN/ckptctl" "$ROOT/cmd/ckptctl"
	go build -o "$BIN/ckpttrainer" "$ROOT/cmd/ckpttrainer"
}

# verify <dir> -> prints the JSON line from verify.py; exit status says whether it survived
verify() { python3 "$RIG/verify.py" "$1"; }

# survivor <dir> -> the step number of the newest intact checkpoint, or -1
survivor() {
	line=$(verify "$1" 2>/dev/null || true)
	printf '%s' "$line" | sed -n 's/.*"step": *\([-0-9]*\).*/\1/p'
}

# record <layer> <scenario> <arm> <json-object>
record() {
	# The fourth argument is an object, and it needs a key: a spliced object makes the line invalid
	# JSON, which turns the trend file into a puzzle instead of a measurement.
	printf '{"layer":"%s","id":"%s","arm":"%s","result":%s}\n' "$1" "$2" "$3" "$4" >>"$RESULTS"
}

# claim <layer> <scenario> <naive-lost> <governed-lost> <naive-ok> <governed-ok> [extra]
#
# The claim is not "we were faster": it is that the baseline loses work and we do not, or that both
# lose the same bounded amount. A run where the baseline also survives is printed as vacuous and
# fails — a fault that does not break the baseline has not tested the product.
claim() {
	layer=$1 scenario=$2 naive_lost=$3 gov_lost=$4 naive_ok=$5 gov_ok=$6 extra=${7:-}
	printf 'exp[%s] %-18s naive lost %5ss (%s) · governed lost %5ss (%s)\n' \
		"$layer" "$scenario" "$naive_lost" "$naive_ok" "$gov_lost" "$gov_ok"
	if [ "$naive_ok" = "1" ] && [ "$gov_ok" = "1" ]; then
		echo "claim vacuous: the baseline also survived, so this fault tested nothing"
		return 1
	fi
	if [ "$gov_ok" != "1" ]; then
		echo "FAIL: the governed arm did not survive either"
		return 1
	fi
	if [ "${naive_lost%.*}" -le "${gov_lost%.*}" ] 2>/dev/null; then
		if [ "${naive_lost%.*}" -eq "${gov_lost%.*}" ] && [ "$naive_ok" = "1" ]; then
			echo "FAIL: no improvement over the baseline"
			return 1
		fi
	fi
	[ -n "$extra" ] && echo "       $extra"
	return 0
}
