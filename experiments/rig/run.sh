#!/bin/sh
# The experiment rig, X layer: is the write safe? Real processes, real files, seconds.
#
#   run.sh l1 kill-during-write   killed in the middle of a write
#   run.sh l1 user-stop           the user stops the job; who gets a last checkpoint, and who is held
#   run.sh l1 disk-full           the volume runs out while the job is writing
#
# Both arms run the same trainer binary, concurrently, so they see the same machine load. The naive
# arm is what a team writes by hand: `--save-every` on a timer plus a copy loop that rotates the
# copies (baseline.sh, B3 by default). The governed arm is the same job with the controller and
# nothing else.
#
# Nothing here sleeps for a fixed time. Faults trigger on the job's own state — "a step directory
# exists without its manifest" means a write is in flight right now — because a wall-clock guess
# makes every run slow and every failure look like a timeout.
set -eu

RIG=$(cd "$(dirname "$0")" && pwd)
. "$RIG/lib.sh"

usage() {
	sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
}

last_step() { cat "$1/.ckpt-governor/step" 2>/dev/null || echo 0; }
lost() { printf '%s' "$(( ($1 - $2) * $3 / 1000 ))"; }

# report_arm <experiment> <arm> <dir> <copies-dir> <step_ms> [extra-json] -> "ok lost survivor"
# Both arms go through this, so the two numbers in a claim can never be computed two ways.
report_arm() {
	id=$1 arm=$2 dir=$3 copies=$4 step_ms=$5 extra=${6:-}
	kill_at=$(last_step "$dir")
	sv=$(survivor "$copies")
	ok=0
	[ "$sv" -ge 0 ] && ok=1
	l=$(lost "$kill_at" "$sv" "$step_ms")
	record l1 "$id" "$arm" "{\"ok\":$ok,\"lost_seconds\":$l,\"survivor\":$sv,\"killed_at\":$kill_at$extra}"
	printf '%s %s %s' "$ok" "$l" "$sv"
}

newest_step() {
	newest=""
	for d in "$1"/step-*; do
		[ -d "$d" ] || continue
		newest="$d"
	done
	printf '%s' "$newest"
}

# midwrite <dir> -> true while a write is in flight: the newest step directory exists, its manifest
# has not landed. That is the window a kill has to hit, and it is observable in milliseconds.
midwrite() {
	d=$(newest_step "$1")
	[ -n "$d" ] || return 1
	[ -f "$d/manifest.json" ] && return 1
	return 0
}

await_midwrite() {
	tries=$(( $2 / 20 ))
	i=0
	while [ "$i" -lt "$tries" ]; do
		midwrite "$1" && return 0
		sleep 0.02
		i=$((i + 1))
	done
	return 1
}

# await_torn <dir> <timeout-ms> -> the job is writing and the copy loop has already taken that
# half-written directory. This is the state in which a stop costs the naive arm work: the copy on
# disk is torn, and the job's own write finishing later cannot repair it. Waiting for it makes the
# round deterministic — without it, a stop that lands after the copier took a *complete* copy leaves
# the baseline whole, and the round measures nothing.
await_torn() {
	tries=$(( $2 / 20 ))
	i=0
	while [ "$i" -lt "$tries" ]; do
		d=$(newest_step "$1")
		if [ -n "$d" ] && [ ! -f "$d/manifest.json" ] && [ -d "$1/.backup/$(basename "$d")" ]; then
			return 0
		fi
		sleep 0.02
		i=$((i + 1))
	done
	return 1
}

# await_sealed <dir> <timeout-ms> -> once the job has at least one intact checkpoint
await_sealed() {
	tries=$(( $2 / 20 ))
	i=0
	while [ "$i" -lt "$tries" ]; do
		[ "$(survivor "$1")" -ge 0 ] && return 0
		sleep 0.02
		i=$((i + 1))
	done
	return 1
}

# stop_arms <dirs...> : kill every background process this scenario started, without `wait`.
# A bare `wait` on a process that ignored TERM is how a two-second experiment becomes a timeout.
reap() {
	for p in "$@"; do
		kill -9 "$p" 2>/dev/null || true
	done
	sleep 0.05
}

# --- kill-during-write ------------------------------------------------------
#
# X1. A 300 ms write and a state-triggered kill, so the kill lands inside a write by construction.
# The naive arm is copying while that write runs; the governed arm is sealing instead.
scenario_kill() {
	step_ms=20
	naive="$TMP/kill/naive"
	gov="$TMP/kill/gov"
	mkdir -p "$naive" "$gov"

	"$BIN/ckpttrainer" --dir "$naive" --steps 100000 --step-ms $step_ms --write-ms 300 \
		--save-every 400ms >/dev/null 2>&1 &
	naive_job=$!
	sh "$RIG/baseline.sh" --dir "$naive" --period 0.2 --mode "${BASELINE:-b3}" >/dev/null 2>&1 &
	naive_copy=$!

	"$BIN/ckptctl" run --dir "$gov" --no-us=none --window=400ms --every=40ms >/dev/null 2>&1 &
	gov_ctl=$!
	"$BIN/ckpttrainer" --dir "$gov" --steps 100000 --step-ms $step_ms --write-ms 300 >/dev/null 2>&1 &
	gov_job=$!

	# One sealed checkpoint first, or "the previous copy survived" would have nothing to survive.
	await_sealed "$naive" 3000 || true
	await_sealed "$gov" 3000 || true

	if await_midwrite "$naive" 3000 && await_midwrite "$gov" 3000; then
		kill -9 "$naive_job" "$gov_job" 2>/dev/null || true
	else
		kill -9 "$naive_job" "$gov_job" 2>/dev/null || true
		echo "note: no write was in flight within 3s; the round is vacuous" >&2
	fi
	reap "$naive_copy" "$gov_ctl"

	n=$(report_arm kill-during-write naive "$naive" "$naive/.backup" $step_ms)
	g=$(report_arm kill-during-write governed "$gov" "$gov" $step_ms)
	set -- $n; naive_ok=$1; naive_lost=$2
	set -- $g; gov_ok=$1; gov_lost=$2
	claim l1 kill-during-write "$naive_lost" "$gov_lost" "$naive_ok" "$gov_ok" \
		"state-triggered kill, both arms writing when it landed"
}

# --- user-stop --------------------------------------------------------------
#
# X3. SIGTERM. The naive arm has no protocol for a stop: it exits the moment it is told, so a write
# that takes 400ms cannot finish and the last period is lost. The governed job waits for the
# controller's `may-exit`, which is only written after the final write has landed.
#
# The stop is aimed at the state where the naive arm has already lost: a write in flight *and* the
# copy loop's snapshot of that half-written directory on disk. Waiting for the governed arm to seal
# first used to be enough to make the round vacuous on a loaded machine: the copier got its next
# snapshot after the naive write had completed, so the baseline survived the stop whole.
scenario_stop() {
	step_ms=20
	write_ms=400
	naive="$TMP/stop/naive"
	gov="$TMP/stop/gov"
	mkdir -p "$naive" "$gov"

	"$BIN/ckpttrainer" --dir "$naive" --steps 100000 --step-ms $step_ms --write-ms $write_ms \
		--grace 0s --save-every 1s >/dev/null 2>&1 &
	naive_job=$!
	sh "$RIG/baseline.sh" --dir "$naive" --period 0.1 --mode "${BASELINE:-b3}" >/dev/null 2>&1 &
	naive_copy=$!

	"$BIN/ckptctl" run --dir "$gov" --no-us=none --window=400ms --every=40ms >/dev/null 2>&1 &
	gov_ctl=$!
	"$BIN/ckpttrainer" --dir "$gov" --steps 100000 --step-ms $step_ms --write-ms $write_ms --grace 5s >/dev/null 2>&1 &
	gov_job=$!

	if ! await_torn "$naive" 5000; then
		echo "note: the baseline never copied a write in flight within 5s; the round is vacuous" >&2
	fi

	kill -TERM "$naive_job" "$gov_job" 2>/dev/null || true

	released=0
	i=0
	while [ "$i" -lt 150 ]; do
		if [ -f "$gov/.ckpt-governor/may-exit" ]; then released=1; break; fi
		sleep 0.02
		i=$((i + 1))
	done
	reap "$naive_job" "$gov_job" "$naive_copy" "$gov_ctl"

	n=$(report_arm user-stop naive "$naive" "$naive/.backup" $step_ms)
	g=$(report_arm user-stop governed "$gov" "$gov" $step_ms ",\"released\":$released")
	set -- $n; naive_ok=$1; naive_lost=$2
	set -- $g; gov_ok=$1; gov_lost=$2
	claim l1 user-stop "$naive_lost" "$gov_lost" "$naive_ok" "$gov_ok" "released=$released"
}

# --- disk-full --------------------------------------------------------------
#
# X2. An 8 MiB volume is the limit. The claim is deliberately not "lost seconds" — a compressed
# timer loses little by construction — but how much room is left when the job dies and whether the
# newest copy still verifies.
#
#   VOLUME=tmpfs (default)  a size-limited tmpfs: real size accounting, no disk timing
#   VOLUME=loop             a loopback ext4 image: a real file system, real fsync and real ENOSPC
#
# The loop variant is the P9 row's "physical disk volume". It still needs root, and the loop device
# is detached by cleanup even when an assertion exits first.
VOLUME=${VOLUME:-tmpfs}
LOOP_MB=${LOOP_MB:-12}
STORE_EVERY=${STORE_EVERY:-200ms}

mount_volume() {
	mnt=$1
	case "$VOLUME" in
	tmpfs)
		mount -t tmpfs -o size=8m tmpfs "$mnt" || return 1
		;;
	loop)
		img="$mnt.img"
		dd if=/dev/zero of="$img" bs=1M count=$LOOP_MB status=none || return 1
		l=$(losetup -f) || return 1
		[ -n "$l" ] || return 1
		losetup "$l" "$img" || return 1
		if ! mkfs.ext4 -q "$l" 2>/dev/null || ! mount "$l" "$mnt"; then
			losetup -d "$l" 2>/dev/null || true
			return 1
		fi
		DISK_LOOPS="$DISK_LOOPS $l"
		;;
	*)
		echo "unknown VOLUME=$VOLUME (tmpfs or loop)" >&2
		return 1
		;;
	esac
	DISK_MOUNTS="$DISK_MOUNTS $mnt"
}

unmount_volumes() {
	for m in $DISK_MOUNTS; do umount "$m" 2>/dev/null || true; done
	for l in $DISK_LOOPS; do losetup -d "$l" 2>/dev/null || true; done
	DISK_MOUNTS=""
	DISK_LOOPS=""
}

scenario_disk_full() {
	step_ms=20
	shard=$((1024 * 1024))
	tmpfree() { df -k "$1" 2>/dev/null | awk 'NR==2 {print $4}'; }

	naive_mnt="$TMP/naive-vol"
	gov_mnt="$TMP/gov-vol"
	mkdir -p "$naive_mnt" "$gov_mnt"
	if ! mount_volume "$naive_mnt" 2>/dev/null; then
		echo "SKIP disk-full: cannot mount a $VOLUME volume here (needs root)"
		record l1 disk-full naive '{"ok":-1,"skipped":"no volume"}'
		return 0
	fi
	mount_volume "$gov_mnt"

	"$BIN/ckpttrainer" --dir "$naive_mnt" --steps 100000 --step-ms $step_ms --write-ms 40 \
		--shard-bytes $shard --ranks 2 --save-every 400ms >/dev/null 2>&1 &
	naive_job=$!
	sh "$RIG/baseline.sh" --dir "$naive_mnt" --period 0.2 --mode "${BASELINE:-b3}" >/dev/null 2>&1 &
	naive_copy=$!

	# The storage condition is read every 200ms here, not at the 5s default: the round is seconds
	# long, and a volume this small can be consumed inside one default interval. The trigger is the
	# product's own rule; only its cadence is scaled to the experiment. STORE_EVERY=5s reproduces
	# that boundary.
	"$BIN/ckptctl" run --dir "$gov_mnt" --no-us=none --window=400ms --every=40ms \
		--store-every=$STORE_EVERY --free-factor=3 >/dev/null 2>&1 &
	gov_ctl=$!
	"$BIN/ckpttrainer" --dir "$gov_mnt" --steps 100000 --step-ms $step_ms --write-ms 40 \
		--shard-bytes $shard --ranks 2 >/dev/null 2>&1 &
	gov_job=$!

	# The naive arm is what has to fill its volume; a real file system's fsync makes that slower than
	# tmpfs, so the loop volume gets a longer wall clock for the same fault.
	tries=200
	[ "$VOLUME" = "loop" ] && tries=2000
	i=0
	while [ "$i" -lt "$tries" ]; do
		n=$(tmpfree "$naive_mnt")
		case "$n" in ''|*[!0-9]*) n=999999 ;; esac
		if [ "$n" -lt 64 ]; then break; fi
		sleep 0.02
		i=$((i + 1))
	done

	kill -9 "$naive_job" "$gov_job" 2>/dev/null || true
	reap "$naive_copy" "$gov_ctl"

	n_free=$(tmpfree "$naive_mnt")
	g_free=$(tmpfree "$gov_mnt")
	case "$n_free" in ''|*[!0-9]*) n_free=0 ;; esac
	case "$g_free" in ''|*[!0-9]*) g_free=0 ;; esac

	n=$(report_arm disk-full naive "$naive_mnt" "$naive_mnt/.backup" $step_ms ",\"free_kb\":$n_free,\"volume\":\"$VOLUME\"")
	g=$(report_arm disk-full governed "$gov_mnt" "$gov_mnt" $step_ms ",\"free_kb\":$g_free,\"volume\":\"$VOLUME\"")
	set -- $n; naive_ok=$1
	set -- $g; gov_ok=$1

	unmount_volumes

	printf 'exp[l1] %-18s (%s) naive: free %5sKiB intact=%s · governed: free %5sKiB intact=%s\n' \
		disk-full "$VOLUME" "$n_free" "$naive_ok" "$g_free" "$gov_ok"
	bad=0
	if [ "$n_free" -ge 64 ]; then
		echo "FAIL disk-full: the naive arm never filled its volume (${n_free}KiB left)"
		bad=1
	fi
	if [ "$g_free" -lt 512 ]; then
		echo "FAIL disk-full: the governed arm also ran its volume down to ${g_free}KiB"
		bad=1
	fi
	if [ "$gov_ok" != "1" ]; then
		echo "FAIL disk-full: the governed arm has no intact checkpoint"
		bad=1
	fi
	[ "$bad" -eq 0 ] && echo "       the volume filled with room for one more copy on our side"
	[ "$bad" -eq 0 ]
}

case "${1:-}" in
l1)
	case "${2:-}" in
	kill-during-write) build; scenario_kill ;;
	user-stop) build; scenario_stop ;;
	disk-full) build; scenario_disk_full ;;
	*) usage ;;
	esac
	;;
*) usage ;;
esac
