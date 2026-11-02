#!/bin/sh
# The demo, in three acts, in about fifteen seconds.
#
#   1  nothing configured      the job writes nothing; nothing was at risk
#   2  a fault                 the platform announces a preemption; we write, once, and say why
#   3  a window you chose      writes every two seconds, and the receipt says what that bought
#
# Act 1 is the one that matters: it looks exactly like a job that never configured checkpointing,
# because that is what most jobs are. Act 2 is the promise. Act 3 is the price.
set -eu

if [ -z "${CKPT_BUDGET_ARMED:-}" ]; then
	export CKPT_BUDGET_ARMED=1
	exec timeout --preserve-status "${BUDGET:-90}" sh "$0" "$@"
fi

HERE=$(cd "$(dirname "$0")" && pwd)
RIG=$(cd "$HERE/../rig" && pwd)
ROOT=$(cd "$RIG/../.." && pwd)
BIN="$RIG/.bin"
WORK=${WORK:-/tmp/ckpt-demo}

# Rebuild when anything the binaries are made of changed. Watching only cmd/ meant a fix in internal/ ran
# against a stale binary — the demo then shows the old behaviour and looks like the fix did not work.
if [ ! -x "$BIN/ckptctl" ] || [ -n "$(find "$ROOT/cmd" "$ROOT/internal" -name '*.go' -newer "$BIN/ckptctl" -print -quit)" ]; then
	mkdir -p "$BIN"
	(cd "$ROOT" && go build -o "$BIN/ckptctl" ./cmd/ckptctl && go build -o "$BIN/ckpttrainer" ./cmd/ckpttrainer)
fi

say() { printf '%s\n' "$*"; }
rule() { printf '%s\n' "------------------------------------------------------------"; }
show() { sed -n '2,$p' "$WORK/$1/log"; }

# world <dir> <announced> : the product writes the world itself (`ckptctl world --fake`), so the
# demo needs nothing but the binary it just built — no python, no shell helpers, no cluster. With 1
# the platform has already set a deletion timestamp, which is the whole announcement.
world() {
	if [ "$2" = "1" ]; then
		"$BIN/ckptctl" world --dir "$1" --cluster a --unit "$1" --fake --announce >/dev/null
	else
		"$BIN/ckptctl" world --dir "$1" --cluster a --unit "$1" --fake >/dev/null
	fi
}

# run_act <name> <seconds> <controller args...>
run_act() {
	name=$1 seconds=$2
	shift 2
	dir="$WORK/$name"
	rm -rf "$dir"; mkdir -p "$dir"
	"$BIN/ckptctl" run --dir "$dir" "$@" >"$dir/log" 2>&1 &
	ctl=$!
	"$BIN/ckpttrainer" --dir "$dir" --steps 100000 --step-ms 20 --write-ms 60 >/dev/null 2>&1 &
	job=$!
	sleep "$seconds"
	kill -TERM "$ctl" 2>/dev/null || true
	wait "$ctl" 2>/dev/null || true
	kill -9 "$job" 2>/dev/null || true
	wait "$job" 2>/dev/null || true
}

rule
say "act 1 - a job with no configuration at all"
rule
run_act no-config 3 --no-us none --every 1s
show no-config
say ""

rule
say "act 2 - the same job, and the platform announces a preemption"
rule
dir="$WORK/preempt"
rm -rf "$dir"; mkdir -p "$dir"
world "$dir" 0
"$BIN/ckptctl" run --dir "$dir" --no-us none --every 1s >"$dir/log" 2>&1 &
ctl=$!
# --contract is the 0025 writer: gen-N with a manifest sealed last, so the thing left on disk can be
# judged. Without it the demo shows a decision and never a verdict.
"$BIN/ckpttrainer" --dir "$dir" --steps 100000 --step-ms 20 --write-ms 60 --contract >/dev/null 2>&1 &
job=$!
sleep 1
say "  ... the platform announces the unit is going"
world "$dir" 1
sleep 7
kill -TERM "$ctl" 2>/dev/null || true
wait "$ctl" 2>/dev/null || true
kill -9 "$job" 2>/dev/null || true
wait "$job" 2>/dev/null || true
show preempt

# The join: the decision above, and then what the disk actually holds. A kill -9 leaves the last
# generation in flight, so this is also the case where the answer is "waiting" rather than "verified".
say "  what the preemption left on disk:"
"$BIN/ckptctl" verify --dir "$dir" --all 2>&1 | sed 's/^/    /'
say ""

rule
say "act 3 - the same job, with a window the user chose"
rule
run_act window 6 --no-us 10m --window 2s --every 1s
show window
say ""

rule
say "the three receipts"
rule
for a in no-config preempt window; do
	say "$a"
	tail -3 "$WORK/$a/log" | sed 's/^/    /'
	say ""
done
