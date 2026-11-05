#!/bin/sh
# The real-clock soak: a job that is preempted again and again, for DURATION seconds (default 2h).
#
# Each cycle is a real controller, a real job, a real SIGTERM, and a real question afterwards:
# "can the copy that landed continue?" A cycle that leaves nothing sealed, or a copy that cannot
# resume, is a failure; the run continues so the count is the evidence.
#
# Usage: DURATION=7200 sh hack/soak.sh   (needs go and a POSIX shell; writes a line per cycle)
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
GO=${GO:-go}
DURATION=${DURATION:-7200}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$WORK/bin"
"$GO" build -o "$WORK/bin/ckptctl" ./cmd/ckptctl
"$GO" build -o "$WORK/bin/ckpttrainer" ./cmd/ckpttrainer

started=$(date +%s)
cycle=0
failures=0
echo "soak: ${DURATION}s · controller window 5s · one preemption per cycle"

while :; do
	now=$(date +%s)
	elapsed=$((now - started))
	[ "$elapsed" -ge "$DURATION" ] && break
	cycle=$((cycle + 1))
	dir="$WORK/cycle-$cycle"
	mkdir -p "$dir"

	"$WORK/bin/ckptctl" run --dir "$dir" --no-us=none --window=5s --every=200ms \
		--teardown=3s >"$dir/ctl.log" 2>&1 &
	ctl=$!
	"$WORK/bin/ckpttrainer" --dir "$dir" --steps 1000000 --step-ms 50 --write-ms 800 \
		--grace 10s --contract >"$dir/job.log" 2>&1 &
	job=$!

	sleep 25
	kill -TERM "$job" 2>/dev/null || true
	i=0
	while [ ! -f "$dir/.ckpt-governor/may-exit" ] && [ "$i" -lt 150 ]; do
		sleep 0.1
		i=$((i + 1))
	done
	if [ ! -f "$dir/.ckpt-governor/may-exit" ]; then
		kill -9 "$job" 2>/dev/null || true
	fi
	kill "$ctl" 2>/dev/null || true
	wait 2>/dev/null || true

	sealed=$(ls -d "$dir"/gen-* 2>/dev/null | while read -r g; do
		[ -f "$g/manifest.json" ] && echo "$g"
	done | wc -l)
	verified=0
	if "$WORK/bin/ckptctl" verify --dir "$dir" --all >"$dir/verify.log" 2>&1; then
		verified=1
	fi
	ok=0
	[ "$sealed" -gt 0 ] && [ "$verified" = 1 ] || failures=$((failures + 1))
	ok=$verified
	printf 'cycle=%d elapsed=%ds sealed=%d verified=%d\n' \
		"$cycle" "$(($(date +%s) - started))" "$sealed" "$ok"
	rm -rf "$dir"
done

echo "soak: $cycle cycles · failures $failures · ${DURATION}s budget"
[ "$failures" -eq 0 ]
