#!/bin/sh
# verify-matrix.sh — real processes, real files, and the verdict each fault earns.
#
# The point is not to prove that writes fail. It is to prove the classifier is right about *how* they fail:
# a short file, a torn tail, a writer that could not finish in time, two writers racing. A fake filesystem
# would be right about a fake.
#
#   sh experiments/rig/verify-matrix.sh            # writes experiments/rig/matrix.csv
#   OUT=/tmp/m.csv sh experiments/rig/verify-matrix.sh
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
out=${OUT:-$root/experiments/rig/matrix.csv}
work=$(mktemp -d)
trap 'umount "$work/unmount" 2>/dev/null || true; rm -rf "$work"' EXIT

cd "$root"
go build -o "$work/ckpttrainer" ./cmd/ckpttrainer
go build -o "$work/ckptctl" ./cmd/ckptctl

failed=0
printf 'fault,line\n' > "$out"

# run <name> <trainer flags...>: write a generation, then ask the classifier what it is.
run() {
	name=$1
	shift
	dir="$work/$name"
	mkdir -p "$dir"
	# The trainer exits non-zero when a write is refused; the matrix is about what remains on disk, so a
	# failed run is a case rather than an error.
	"$work/ckpttrainer" --dir="$dir" "$@" >/dev/null 2>&1 || true

	line=$("$work/ckptctl" verify --dir="$dir" --all 2>&1 | sed -n '1p')
	# One row, one line, commas neutralised: a column built from a second `sed` line is blank exactly on the
	# rows that pass, and a CSV whose columns shift by row is trusted for a month and then breeds a bad chart.
	printf '%s,%s\n' "$name" "$(echo "${line:-no-generation}" | tr ',' ';')" >> "$out"
	printf '%-14s %s\n' "$name" "${line:-no-generation}"
	expect "$name" "${line:-no-generation}"
}

# expect <fault> <line>: the rows whose verdict is a fact about the writer, not about the race.
#
# `dual-writer` is deliberately absent. Its verdict depends on which writer sealed last, and a gate that
# fails half the time gets switched off — the row is recorded, and the finding carries the reasoning.
expect() {
	case $1 in
	clean) want=verified ;;
	torn-tail) want=hash ;;
	slow-fsync) want=verified ;;
	esac
	[ -n "${want:-}" ] || return 0
	if ! echo "$2" | grep -q "$want"; then
		printf 'MISMATCH %s: want %s, got: %s\n' "$1" "$want" "$2"
		failed=$((failed + 1))
	fi
	want=
}

common="--steps=2 --step-ms=60 --write-ms=40 --shard-bytes=65536 --ranks=2 --contract --save-every=200ms"

# The ordinary case: everything lands, the manifest is last.
# shellcheck disable=SC2086
run clean $common

# A kill between two syscalls: right size, wrong last block. The declared tail hash is what catches it.
# shellcheck disable=SC2086
run torn-tail $common --tear

# A writer that pays many real fsyncs: slower, and still whole. If this one broke, the deadline would be
# testing the timer instead of the disk.
# shellcheck disable=SC2086
run slow-fsync $common --fsync-ms=100

# Two writers, one generation. The contract says one write per volume at a time; this asks what a deployment
# that ignores it leaves behind. They are given different shard sizes on purpose, so one writer's manifest
# cannot describe the other writer's bytes: the only safe answer is "not verified".
race() {
	dir="$work/dual-writer"
	mkdir -p "$dir"
	"$work/ckpttrainer" --dir="$dir" --steps=2 --step-ms=60 --write-ms=120 --shard-bytes=65536 \
		--ranks=2 --contract --save-every=150ms >/dev/null 2>&1 &
	one=$!
	"$work/ckpttrainer" --dir="$dir" --steps=2 --step-ms=60 --write-ms=120 --shard-bytes=98304 \
		--ranks=3 --contract --save-every=150ms >/dev/null 2>&1 &
	two=$!
	wait "$one" || true
	wait "$two" || true

	line=$("$work/ckptctl" verify --dir="$dir" --all 2>&1 | sed -n '1p')
	printf 'dual-writer,%s\n' "$(echo "${line:-no-generation}" | tr ',' ';')" >> "$out"
	printf '%-14s %s\n' "dual-writer" "${line:-no-generation}"
}

race

# The volume goes away while the writer is writing. This is not a kill: the process is alive and its next
# write fails with EIO. The interesting answers are "did it hang" and "what does the classifier say
# afterwards", because a directory that no longer exists is not the same as a directory that is being written.
unmount() {
	mnt="$work/unmount"
	mkdir -p "$mnt"
	if ! mount -t tmpfs -o size=16m tmpfs "$mnt" 2>/dev/null; then
		printf 'unmount,skipped,needs mount privileges\n' >> "$out"
		printf '%-14s skipped (needs mount privileges)\n' "unmount"
		return
	fi

	"$work/ckpttrainer" --dir="$mnt" --steps=400 --step-ms=20 --write-ms=500 \
		--shard-bytes=1048576 --ranks=2 --contract --save-every=120ms >/dev/null 2>&1 &
	pid=$!

	# Wait for a generation directory with no manifest: that is a write in flight, by construction.
	i=0
	while [ "$i" -lt 200 ]; do
		for d in "$mnt"/gen-*; do
			[ -d "$d" ] || continue
			if [ ! -f "$d/manifest.json" ]; then
				break 2
			fi
		done
		i=$((i + 1))
		sleep 0.05
	done

	# What we say while it is still writing: anything but `verified`, because nothing has been sealed.
	during=$("$work/ckptctl" verify --dir="$mnt" --all 2>&1 | sed -n '1p')

	# Record whether the volume actually left. A plain unmount is refused while the writer holds files open
	# — the kernel protecting us — and a scenario that proceeds through a refused unmount measures nothing.
	# A lazy detach is the one that succeeds, and it is the interesting case: the path disappears.
	if umount "$mnt" 2>/dev/null; then
		detach=unmounted
	elif umount -l "$mnt" 2>/dev/null; then
		detach=lazy-detach
	else
		detach=none
	fi

	rc=0
	wait "$pid" 2>/dev/null || rc=$?

	after=$("$work/ckptctl" verify --dir="$mnt" --all 2>&1 | sed -n '1p')
	payload="after=[${after:-no-generation}] during=[${during:-none}] detach=$detach writer_exit=$rc"
	printf 'unmount,%s\n' "$(echo "$payload" | tr ',' ';')" >> "$out"
	printf '%-14s %s  (during: %s, detach: %s, writer exit %s)\n' \
		"unmount" "${after:-no-generation}" "${during:-none}" "$detach" "$rc"

	# A volume that vanished must leave nothing that verifies, and the writer must say so.
	if echo "${after:-}" | grep -q verified; then
		printf 'MISMATCH unmount: a copy verified after the volume was detached\n'
		failed=$((failed + 1))
	fi
	if [ "$detach" != none ] && [ "$rc" -eq 0 ]; then
		printf 'MISMATCH unmount: the writer exited 0 after losing its volume\n'
		failed=$((failed + 1))
	fi
}

unmount


# The volume runs out mid-write. Not a kill and not a corrupt byte: ENOSPC, which the product has to survive
# without losing the copy that was already good.
space() {
	mnt="$work/space"
	mkdir -p "$mnt"
	if ! mount -t tmpfs -o size=24m tmpfs "$mnt" 2>/dev/null; then
		printf 'space,skipped,needs mount privileges\n' >> "$out"
		printf '%-14s skipped (needs mount privileges)\n' "space"
		return
	fi

	# 4 MiB per shard, two shards: 8 MiB per copy, so the third copy cannot land in 24 MiB.
	rc=0
	"$work/ckpttrainer" --dir="$mnt" --steps=60 --step-ms=40 --write-ms=80 \
		--shard-bytes=4194304 --ranks=2 --contract --save-every=100ms >/dev/null 2>&1 || rc=$?

	line=$("$work/ckptctl" verify --dir="$mnt" --all 2>&1 | sed -n '1p')
	gen=$("$work/ckptctl" verify --dir="$mnt" --all 2>&1 | grep -c 'verified' || true)

	umount "$mnt" 2>/dev/null || true

	printf 'space,after=[%s] verified=%s writer_exit=%s\n' \
		"${line:-no-generation}" "$gen" "$rc" | sed 's/,/;/;s/,/;/' >> "$out"
	printf '%-14s %s  (verified %s, writer exit %s)\n' "space" "${line:-no-generation}" "$gen" "$rc"

	# A full volume must leave the writer saying so, and must not leave a half copy looking whole.
	if [ "$rc" -eq 0 ]; then
		printf 'MISMATCH space: the writer exited 0 although the volume filled\n'
		failed=$((failed + 1))
	fi
	if [ "$gen" -eq 0 ]; then
		printf 'MISMATCH space: no copy survived the full volume\n'
		failed=$((failed + 1))
	fi
}

space

printf '\nresults in %s\n' "$out"
if [ "${failed:-0}" -gt 0 ]; then
	printf '%d row(s) did not match what the classifier promises\n' "$failed"
	exit 1
fi
printf 'every deterministic row matched\n' 
