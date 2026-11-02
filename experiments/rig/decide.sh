#!/bin/sh
# The D layer: does the system DECIDE to write when a fault arrives? Milliseconds, no write at all.
#
#   decide.sh all            every case: injection + one round, eight of them
#   decide.sh d-preempt      one case
#
# A case injects the state a fault produces — a world snapshot, a unit record, a glue file, a broken
# directory — runs ONE round, and reads the published plan. It never starts a training job and never
# writes a checkpoint: "we should write" and "the write is safe" are different claims, and this layer
# only makes the first one (docs/experiments-matrix.md §0).
set -eu

RIG=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$RIG/../.." && pwd)
. "$RIG/lib.sh"

PERIOD=${BASELINE_PERIOD:-600}   # the baseline's checkpoint period, in seconds
ROUND_FLAGS=${ROUND_FLAGS:-}     # extra flags for one round, set by the case

now_ns() { date +%s%N; }

# round <dir> -> the round's latency in milliseconds
round() {
	start=$(now_ns)
	# shellcheck disable=SC2086
	"$BIN/ckptctl" run --once --dir "$1" --no-us=none $ROUND_FLAGS >/dev/null 2>&1 || true
	end=$(now_ns)
	printf '%s' "$(( (end - start) / 1000000 ))"
}

plan() { "$BIN/ckptctl" explain --dir "$1" 2>&1; }

# assert_decision <case> <dir> <want-action> <want-substring|-> <latency> [note]
assert_decision() {
	id=$1 dir=$2 want=$3 substr=$4 ms=$5
	out=$(plan "$dir")
	got=$(printf '%s' "$out" | sed -n 's/^decision   //p')
	case "$got" in
	"$want"*) ;;
	*)
		echo "FAIL $id: decision = '$got', want '$want'"
		printf '%s\n' "$out" | sed -n 's/^why        /  why: /p'
		record l1 "$id" ours "{\"ok\":0,\"latency_ms\":$ms}"
		return 1
		;;
	esac
	if [ "$substr" != "-" ]; then
		case "$out" in
		*"$substr"*) ;;
		*)
			echo "FAIL $id: the plan does not mention '$substr'"
			printf '%s\n' "$out" | sed -n 's/^why        /  why: /p'
			record l1 "$id" ours "{\"ok\":0,\"latency_ms\":$ms}"
			return 1
			;;
		esac
	fi
	printf '%-16s decision=%-4s rounds=1 latency=%sms · %s\n' "$id" "$want" "$ms" "${6:-}"
	record l1 "$id" ours "{\"ok\":1,\"latency_ms\":$ms,\"decision\":\"$want\"}"
}

# --- state builders ---------------------------------------------------------

# fixture_checkpoint <dir> <bytes> : a sealed-looking checkpoint, so the storage rules have a scale.
# It is setup, not a run: the decision needs a size, and generating a real one would cost a write.
fixture_checkpoint() {
	python3 - "$1" "$2" <<'PY'
import hashlib, json, os, sys
d, size = sys.argv[1], int(sys.argv[2])
step = os.path.join(d, "step-0")
os.makedirs(step, exist_ok=True)
data = b"\0" * size
open(os.path.join(step, "rank-0.bin"), "wb").write(data)
json.dump({"shards": [{"bytes": size, "rank": 0, "sha256": hashlib.sha256(data).hexdigest()}], "step": 0},
          open(os.path.join(step, "manifest.json"), "w"))
PY
}

# world <dir> [cordoned yes|no] [cpu%] [pending] [announced yes|no]
world() {
	python3 - "$1" "${2:-no}" "${3:-10}" "${4:-0}" "${5:-no}" <<'PY'
import datetime, hashlib, json, os, sys
d, cordoned = sys.argv[1], sys.argv[2] == "yes"
cpu_pct, pending, announced = int(sys.argv[3]), int(sys.argv[4]), sys.argv[5] == "yes"
now = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
w = os.path.join(d, "world", "a")
os.makedirs(os.path.join(w, "units"), exist_ok=True)
json.dump({"schema": 1, "cluster": "a", "at": now, "staleAfterSeconds": 30,
           "capabilities": {"preemption": {"exists": True, "source": "test"},
                            "eviction": {"exists": True, "source": "test"}},
           "nodes": [{"name": "worker-1",
                      "allocatable": {"cpuMilli": 24000, "memBytes": 17179869184, "gpuCount": 8},
                      "requested": {"cpuMilli": 24000 * cpu_pct // 100, "memBytes": 8589934592, "gpuCount": 0},
                      "cordoned": cordoned, "pendingHigherPriority": pending}]},
          open(os.path.join(w, "summary.json"), "w"), indent=2)
unit = os.path.basename(os.path.abspath(d))
json.dump({"schema": 1, "cluster": "a", "unitId": unit, "at": now, "node": "worker-1",
           "priority": 100, "qos": "Burstable",
           "deletionTimestamp": now if announced else None, "evicted": False},
          open(os.path.join(w, "units", hashlib.sha1(unit.encode()).hexdigest() + ".json"), "w"), indent=2)
PY
}

glue() { mkdir -p "$1/.ckpt-governor"; printf '%s' "$2" >"$1/.ckpt-governor/$3"; }

# --- the eight cases --------------------------------------------------------

d_preempt() {
	d="$TMP/d-preempt"; mkdir -p "$d"
	world "$d" no 10 0 yes
	ms=$(round "$d")
	assert_decision d-preempt "$d" save risk.preempt "$ms" \
		"baseline has no reason to write here; its next tick is ${PERIOD}s away"
}

d_eviction() {
	d="$TMP/d-eviction"; mkdir -p "$d"
	world "$d" no 92 2 no
	ms=$(round "$d")
	assert_decision d-eviction "$d" save risk.eviction-likely "$ms" \
		"node at 92% with 2 higher-priority pods waiting"
}

d_cordoned() {
	# A cordon only stops new pods from arriving. It does not take the ones already running: the death is
	# the drain, and the drain announces itself. This case used to expect `save` from the inference on a
	# cordon alone, which is the conflation 0023 removed — and which the Kueue sweep measured: a waiting
	# arrival with room to spare takes nobody.
	d="$TMP/d-cordoned"; mkdir -p "$d"
	world "$d" yes 10 0 no
	ms=$(round "$d")
	assert_decision d-cordoned "$d" skip - "$ms" \
		"a cordon alone takes nobody; nothing says this work is at risk yet"

	# The same node once the drain speaks. Now the death is a fact, and the answer is the write.
	d="$TMP/d-cordoned-announced"; mkdir -p "$d"
	world "$d" yes 10 0 yes
	ms=$(round "$d")
	assert_decision d-cordoned-announced "$d" save - "$ms" \
		"the drain announced itself: that is the death, and it is worth a write"
}

d_low_space() {
	d="$TMP/d-low-space"; mkdir -p "$d"
	fixture_checkpoint "$d" 65536
	# The rule is free < factor x one checkpoint. The factor is large enough to exceed the free space
	# of any real volume, which is how the test says "this volume is about to fill" without filling
	# the disk it runs on.
	ROUND_FLAGS="--free-factor=1000000000"
	ms=$(round "$d")
	ROUND_FLAGS=""
	assert_decision d-low-space "$d" save store.low-space "$ms" "the volume is about to fill, so write now"
}

d_user_stop() {
	d="$TMP/d-user-stop"; mkdir -p "$d"
	glue "$d" 1 stopping
	ms=$(round "$d")
	assert_decision d-user-stop "$d" save risk.shutdown "$ms" "b3 also saves here — this is the parity row"
}

# The v1 promise, as a decision: with nothing configured and nothing happening, we say no, every
# round. The write at the end of training is the job's, not a decision; what this case shows is that
# zero config costs zero writes while the world is calm — the sentence the README is allowed to make.
d_default_calm() {
	d="$TMP/d-default-calm"; mkdir -p "$d"
	# No world, no glue, no config: exactly what a first run looks like.
	skip=0
	total=0
	ms=0
	i=0
	while [ "$i" -lt 3 ]; do
		t=$(round "$d")
		ms=$((ms + t))
		out=$(plan "$d")
		case "$out" in
		*"decision   skip"*) skip=$((skip + 1)) ;;
		esac
		total=$((total + 1))
		i=$((i + 1))
	done
	if [ "$skip" -ne "$total" ]; then
		echo "FAIL d-default-calm: $((total - skip)) of $total rounds wanted to write with nothing configured"
		plan "$d" | sed -n 's/^why        /  why: /p'
		record l1 d-default-calm ours "{\"ok\":0,\"rounds\":$total,\"skips\":$skip}"
		return 1
	fi
	printf '%-16s decision=%-4s rounds=%s latency=%sms · zero config, calm world, not one write\n' \
		d-default-calm skip "$total" "$ms"
	record l1 d-default-calm ours "{\"ok\":1,\"rounds\":$total,\"skips\":$skip,\"latency_ms\":$ms}"
}

# The declaration says the platform announces preemptions; the observer has never written. That is a
# silent channel, not a calm platform: the governor has to say it cannot read the condition, and a
# backstop that cannot see writes. Before this case it reported "absent" and stayed quiet, which is
# the one failure mode the whole product exists to avoid.
d_declared_blind() {
	d="$TMP/d-declared-blind"
	mkdir -p "$d/.ckpt-governor"
	printf '{"schema":1,"capabilities":{"preemption":{"exists":true}}}' >"$d/.ckpt-governor/deployment.json"
	ms=$(round "$d")
	assert_decision d-declared-blind "$d" save risk.preempt "$ms" "a declared channel that is silent is not a calm platform"
}

d_no_time() {
	d="$TMP/d-no-time"; mkdir -p "$d"
	# A deadline only matters when something wants to write: no-time is the gate refusing a save,
	# not a condition of its own.
	glue "$d" 1 stopping
	glue "$d" 2 seconds-left
	# A write is estimated at 30s and the job has 2s left: the right answer is not to start one.
	ROUND_FLAGS="--estimate=30s"
	ms=$(round "$d")
	ROUND_FLAGS=""
	assert_decision d-no-time "$d" skip no-time "$ms" "refused on arithmetic, not on a guess"
}

d_blocked() {
	d="$TMP/d-blocked"; mkdir -p "$d"
	# A read-only checkpoint volume, which even root cannot write around: an immutable directory on a
	# filesystem that supports it. The governor subdirectory is created *before* the freeze, so the
	# controller can still keep its own state — that is the real shape of the failure: the volume that
	# must accept shards refuses, while the controller is perfectly healthy and has to say so.
	d="${TMPDIR:-/tmp}/.ckpt-rig/d-blocked"
	rm -rf "$d"
	mkdir -p "$d/.ckpt-governor"
	if ! chattr +i "$d" 2>/dev/null; then
		echo "SKIP d-blocked: this filesystem does not support immutable directories"
		record l1 d-blocked ours '{"ok":-1,"skipped":"no immutable directory support"}'
		return 0
	fi
	start=$(now_ns)
	out=$("$BIN/ckptctl" run --once --dir "$d" --no-us=none 2>&1 || true)
	end=$(now_ns)
	chattr -i "$d" 2>/dev/null || true
	ms=$(( (end - start) / 1000000 ))
	# store.blocked is a warn-tier condition on purpose: when the volume refuses writes, a write
	# cannot help. The claim is not "we save anyway", it is "we say so instead of trying".
	rc=0
	assert_decision d-blocked "$d" skip store.blocked "$ms" \
		"a read-only volume: it reports instead of trying to write" || rc=1
	rm -rf "$d"
	return $rc
}

d_no_capability() {
	d="$TMP/d-no-capability"; mkdir -p "$d/.ckpt-governor"
	world "$d" no 10 0 yes
	printf '{"schema":1,"capabilities":{"preemption":{"exists":false,"why":"no scheduler"}}}\n' \
		>"$d/.ckpt-governor/deployment.json"
	ms=$(round "$d")
	out=$(plan "$d")
	case "$out" in
	*"decision   save"*)
		echo "FAIL d-no-capability: an announcement produced a save where the deployment declared none"
		record l1 d-no-capability ours "{\"ok\":0,\"latency_ms\":$ms}"
		return 1
		;;
	esac
	printf '%-16s decision=%-4s rounds=1 latency=%sms · the declaration beat the evidence\n' \
		d-no-capability skip "$ms"
	record l1 d-no-capability ours "{\"ok\":1,\"latency_ms\":$ms,\"decision\":\"skip\"}"
}

run_one() {
	build
	case "$1" in
	d-preempt) d_preempt ;;
	d-eviction) d_eviction ;;
	d-cordoned) d_cordoned ;;
	d-low-space) d_low_space ;;
	d-user-stop) d_user_stop ;;
	d-no-time) d_no_time ;;
	d-blocked) d_blocked ;;
	d-no-capability) d_no_capability ;;
	d-declared-blind) d_declared_blind ;;
	d-default-calm) d_default_calm ;;
	*) sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'; return 2 ;;
	esac
}

if [ "${1:-}" = "all" ]; then
	build
	failed=0
	ran=0
	for c in d-preempt d-eviction d-cordoned d-low-space d-user-stop d-no-time d-blocked d-no-capability d-default-calm d-declared-blind; do
		ran=$((ran + 1))
		run_one "$c" || failed=$((failed + 1))
	done
	echo "D layer: $ran cases, $failed failed"
	[ "$failed" -eq 0 ]
else
	run_one "${1:-}"
fi
