#!/bin/sh
# The demo, in four acts, in about twenty seconds.
#
#   1  nothing configured      the job writes nothing; nothing was at risk
#   2  a fault                 the platform announces a preemption; we write, once, and say why
#   3  a window you chose      writes every two seconds, and the receipt says what that bought
#   4  the machine is taken    the job comes back, adopts the seal, and a drill proves it
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

if [ ! -x "$BIN/ckptctl" ] || [ -n "$(find "$ROOT/cmd" -name '*.go' -newer "$BIN/ckptctl" -print -quit)" ]; then
	mkdir -p "$BIN"
	(cd "$ROOT" && go build -o "$BIN/ckptctl" ./cmd/ckptctl && go build -o "$BIN/ckpttrainer" ./cmd/ckpttrainer)
fi
# The synthetic loader: one step, no torch, so act 4 is seconds on any runner.
(cd "$ROOT" && go build -o "$BIN/drill-loader" ./experiments/rig/tools/drill-loader/main.go)

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
"$BIN/ckpttrainer" --dir "$dir" --steps 100000 --step-ms 20 --write-ms 60 >/dev/null 2>&1 &
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
say ""

rule
say "act 3 - the same job, with a window the user chose"
rule
run_act window 6 --no-us 10m --window 2s --every 1s
show window
say ""

# The other half: the platform took the machine, and the job came back. The checkpoint here is a real
# writer's tree (the same fixture the classifier is tested against), the adoption is the three files a
# resumed job writes, and the drill is the one thing that earns the word.
rule
say "act 4 - the machine was taken, and the job comes back"
rule
dir="$WORK/resume"
rm -rf "$dir"
cp -R "$ROOT/internal/resume/testdata/full" "$dir"
say "  can this be resumed?"
"$BIN/ckptctl" resumable --dir "$dir" | sed 's/^/    /'
say "  the job comes back and says what it adopted:"
mkdir -p "$dir/.ckpt-governor"
printf 512 >"$dir/.ckpt-governor/resumed-generation"
printf 512 >"$dir/.ckpt-governor/resumed-step"
printf 9.1 >"$dir/.ckpt-governor/load-seconds"
"$BIN/ckptctl" resumed --dir "$dir" | sed 's/^/    /'
say "  and the only check that proves it:"
"$BIN/ckptctl" drill --dir "$dir" --loader="$BIN/drill-loader" | sed 's/^/    /'
say ""

rule
say "the three receipts"
rule
for a in no-config preempt window; do
	say "$a"
	tail -3 "$WORK/$a/log" | sed 's/^/    /'
	say ""
done
