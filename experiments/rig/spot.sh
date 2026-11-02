#!/bin/sh
# The spot scenario: the platform takes the machine away.
#
#   spot.sh [grace-seconds] [step-ms]
#
# A trainer runs under the governor. The platform sends SIGTERM, waits out the grace period, and then
# kills the process. The claim is one sentence a a person can paste:
#
#   preemption at step N → one write → may-exit written
#
# It fails when the preemption produced no write, when it produced more than one, or when the job was
# not released (may-exit missing). This is the fault the product exists for; every other scenario in
# this directory is a rehearsal for it.
set -eu

RIG=$(cd "$(dirname "$0")" && pwd)
. "$RIG/lib.sh"

grace=${1:-10}
step_ms=${2:-20}
build

dir="$TMP/spot"
mkdir -p "$dir"

step_of() { cat "$dir/.ckpt-governor/step" 2>/dev/null || echo 0; }
sealed() { ls -d "$dir"/step-* 2>/dev/null | wc -l | tr -d ' '; }

# The governor runs for the whole scenario. The teardown and the write cost are declared here because
# the fake trainer's numbers are known: a platform grace of three seconds has to cover one write plus
# the margin, or the honest answer is `no-time` and no write at all.
"$BIN/ckptctl" run --dir="$dir" --unit=job-1 --every=100ms --no-us=none \
	--teardown=200ms --estimate=250ms >"$TMP/run.log" 2>&1 &
gov_pid=$!
trap 'kill "$gov_pid" 2>/dev/null || true; cleanup' EXIT INT TERM

# The job: slow enough to be killed mid-run, fast enough for a rig.
"$BIN/ckpttrainer" --dir="$dir" --steps=400 --step-ms="$step_ms" \
	--write-ms=200 --shard-bytes=1024 --ranks=1 --grace="${grace}s" >"$TMP/trainer.log" 2>&1 &
job_pid=$!

# Trigger on the job's state, never on a wall clock: the preemption is announced once the job is
# running, which is what a platform does.
i=0
while [ "$(step_of)" -lt 5 ]; do
	i=$((i + 1))
	[ "$i" -lt 200 ] || { echo "FAIL: the job never reached step 5"; exit 1; }
	sleep 0.02
done
announced=$(step_of)

kill -TERM "$job_pid" 2>/dev/null || true
# Wait for the grace period to run out. The trainer is expected to keep training, write once and
# release itself; if it is still alive after the grace, that is the failure this scenario reports.
wait "$job_pid" 2>/dev/null || true

writes=$(sealed)
released=0
[ -f "$dir/.ckpt-governor/may-exit" ] && released=1
# The cause comes from the attempt record, not from the plan: the plan is overwritten by the next
# decision (usually a calm skip), while the attempt keeps the reason it was made for.
cause=$(sed -n 's/.*"Reason": "\([^"]*\)".*/\1/p' "$dir/.ckpt-governor/attempts.json" 2>/dev/null | tail -1)
cost=$(sed -n 's/.*"writeSecondsTotal": *\([0-9.]*\).*/\1/p' "$dir/.ckpt-governor/internal.json" 2>/dev/null | head -1)
newest=$(ls -d "$dir"/step-* 2>/dev/null | sed 's|.*/step-||' | sort -n | tail -1)

record l1 spot governed "{\"ok\":$released,\"writes\":$writes,\"announced_at\":$announced,\"cause\":\"${cause:-unknown}\"}"

printf 'spot preemption at step %s (grace %ss)\n' "$announced" "$grace"
printf '  writes: %s, newest sealed step: %s, measured cost: %ss\n' "$writes" "${newest:-none}" "${cost:-0}"
printf '  decision: %s\n' "${cause:-unknown}"
printf '  may-exit: %s\n' "$([ "$released" = 1 ] && echo written || echo missing)"

if [ "$writes" != "1" ]; then
	echo "FAIL: want exactly one write after a preemption, got $writes"
	exit 1
fi
if [ "$released" != "1" ]; then
	echo "FAIL: the job was not released (may-exit missing)"
	exit 1
fi
if [ "${cause:-}" != "risk.shutdown" ] && [ "${cause:-}" != "job.ending" ]; then
	echo "FAIL: the write was not caused by the preemption (cause ${cause:-unknown})"
	exit 1
fi
