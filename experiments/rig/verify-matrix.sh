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
trap 'rm -rf "$work"' EXIT

cd "$root"
go build -o "$work/ckpttrainer" ./cmd/ckpttrainer
go build -o "$work/ckptctl" ./cmd/ckptctl

printf 'fault,verdict,detail\n' > "$out"

# run <name> <trainer flags...>: write a generation, then ask the classifier what it is.
run() {
	name=$1
	shift
	dir="$work/$name"
	mkdir -p "$dir"
	# The trainer exits non-zero when a write is refused; the matrix is about what remains on disk, so a
	# failed run is a case rather than an error.
	"$work/ckpttrainer" --dir="$dir" "$@" >/dev/null 2>&1 || true

	verdict=$("$work/ckptctl" verify --dir="$dir" --all 2>&1 | sed -n '1p')
	detail=$("$work/ckptctl" verify --dir="$dir" --all 2>&1 | sed -n '2p' | sed 's/^ *//')
	printf '%s,%s,%s\n' "$name" "${verdict:-no-generation}" "${detail:-}" >> "$out"
	printf '%-14s %s\n' "$name" "${verdict:-no-generation}"
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

	verdict=$("$work/ckptctl" verify --dir="$dir" --all 2>&1 | sed -n '1p')
	detail=$("$work/ckptctl" verify --dir="$dir" --all 2>&1 | sed -n '2p' | sed 's/^ *//')
	printf 'dual-writer,%s,%s\n' "${verdict:-no-generation}" "${detail:-}" >> "$out"
	printf '%-14s %s\n' "dual-writer" "${verdict:-no-generation}"
}

race

printf '\nresults in %s\n' "$out"
