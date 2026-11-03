#!/bin/sh
# S2: the announced-stop chain, measured at the deadline.
#
# One cell = one arm, one grace, one engine cadence. Each trial starts a real job, sends a real
# SIGTERM once the job has done SIGNAL_STEP steps, and scores what landed: margin before the deadline,
# lost steps, and whether the copy is resumable. `default500` is the counterfactual — a cadence and no
# handler.
#
#   sh experiments/real/s2_shutdown.sh              # 5 trials per cell
#   TRIALS=20 sh experiments/real/s2_shutdown.sh    # the same cells, more trials
#
# The cadence arm's interval is scaled to the trial (a 20-second job never reaches 500 steps). The row
# records the interval, and the claim tested is "a cadence loses up to one interval", not the number
# 500 itself.
set -eu

cd "$(dirname "$0")/../.."
HERE=$(pwd)
PY=${PYTHON:-/root/llamafactory-venv/bin/python}
GO=${GO:-go}
TRIALS=${TRIALS:-5}
SIGNAL_STEP=${SIGNAL_STEP:-120}
SIGNAL_TIMEOUT=${SIGNAL_TIMEOUT:-90}
STEPS=${STEPS:-600}
SEQ=${SEQ:-256}
BATCH=${BATCH:-1}
SAVE_EVERY=${SAVE_EVERY:-100}
# The controller answers `may-exit` after it has seen the final write, not when the job writes it.
# The first runs used 2s and reported "sealed, no may-exit" for a chain that had worked; 20s is the
# wait the handshake actually needs on this box (measured in P1).
WAIT_AFTER_SEAL=${WAIT_AFTER_SEAL:-20}
GRACES=${GRACES:-"60 30 10"}
CADENCES=${CADENCES:-"5s 1s"}
# Which arms to run. The counterfactual does not care about the deadline; a boundary run can skip it.
ARMS=${ARMS:-"us default500"}
DIR_ROOT=${DIR_ROOT:-/root/tmp/s2}
mkdir -p "$DIR_ROOT" "$HERE/experiments/real/bin" "$HERE/experiments/real/results"

# One engine build, used both by the client (bundled _bin) and by the score (resumable).
"$GO" build -trimpath -o "$HERE/experiments/real/bin/ckptctl" ./cmd/ckptctl
cp "$HERE/experiments/real/bin/ckptctl" "$HERE/contrib/ckpt-policy/ckpt_policy/_bin/ckptctl"
export CKPTCTL_ENGINE="$HERE/experiments/real/bin/ckptctl"

rm -f "$HERE/experiments/real/results/s2.jsonl"

steps_done() {
	"$PY" - "$1" <<'PY'
import json, sys
try:
    with open(sys.argv[1]) as f:
        print(int(json.load(f).get("steps_done") or 0))
except Exception:
    print(0)
PY
}

run_one() {
	arm=$1 grace=$2 every=$3 trial=$4
	dir="$DIR_ROOT/$arm-$grace-$every-$trial"
	rm -rf "$dir"
	"$PY" experiments/real/s2_job.py \
		--dir="$dir" --arm="$arm" --grace="$grace" --every="$every" \
		--steps="$STEPS" --seq="$SEQ" --batch="$BATCH" --save-every="$SAVE_EVERY" \
		--wait-after-seal="$WAIT_AFTER_SEAL" \
		> "$dir.log" 2>&1 &
	pid=$!

	# Signal by progress, not by wall clock: model load varies by seconds and would make trials
	# incomparable.
	i=0
	while [ "$i" -lt "$SIGNAL_TIMEOUT" ]; do
		now=$(steps_done "$dir/job.json")
		if [ "$now" -ge "$SIGNAL_STEP" ]; then break; fi
		if ! kill -0 "$pid" 2>/dev/null; then break; fi
		sleep 1
		i=$((i + 1))
	done
	kill -TERM "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
	"$PY" experiments/real/s2_score.py "$dir" "$trial"
	mkdir -p "$HERE/experiments/real/results/logs"
	cp "$dir/.ckpt-governor/engine.log" \
		"$HERE/experiments/real/results/logs/$arm-$grace-$every-$trial.log" 2>/dev/null || true
	# One checkpoint is five gigabytes of dirty page cache. Without a flush between trials the next
	# job's steps crawl on write-back, which would look like a slow governor.
	sync
	sleep 2
	rm -rf "$dir"
}

for arm in $ARMS; do
	for grace in $GRACES; do
		for every in $CADENCES; do
			trial=1
			while [ "$trial" -le "$TRIALS" ]; do
				run_one "$arm" "$grace" "$every" "$trial"
				trial=$((trial + 1))
			done
		done
	done
done

"$PY" - "$HERE/experiments/real/results/s2.jsonl" <<'PY'
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1])]
print("\ncell                  landed/N   margin min..max   lost max   reason")
cells = {}
for r in rows:
    key = (r["arm"], r["grace"], r["every"])
    cells.setdefault(key, []).append(r)
for (arm, grace, every), rs in sorted(cells.items()):
    landed = sum(1 for r in rs if (r["margin_s"] or -1) > 0)
    ms = [r["margin_s"] for r in rs if r["margin_s"] is not None]
    lost = max(r["lost_steps"] for r in rs)
    reasons = sorted({(r.get("reason_at_request") or {}).get("reason") or "-" for r in rs})
    m = f"{min(ms):+.1f}..{max(ms):+.1f}" if ms else "-"
    print(f"{arm:9s} grace={grace:>2}s every={every:>3s}  {landed}/{len(rs):<6} {m:>16}  {lost:<10} {','.join(reasons)}")
PY

printf '\nresults: %s\n' "$HERE/experiments/real/results/s2.jsonl"
