#!/bin/sh
# The end-to-end proof for `ckptctl wrap`: a real controller, a real job, a real SIGTERM.
#
# A job write that takes longer than the grace cannot land inside it, so grace 1 and 10 must print the
# honest failure and grace 30/60 must print the seal. Three runs per grace, because a wrapper that
# worked once is not a protocol.
#
# Usage: sh hack/wrap-grace.sh   (needs go on PATH; about four minutes)
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
GO=${GO:-go}
cd "$root"
$GO build -o "$work/ckptctl" ./cmd/ckptctl
$GO build -o "$work/ckpttrainer" ./cmd/ckpttrainer

write_ms=12000
bad=0
for grace in 60 30 10 1; do
	for run in 1 2 3; do
		dir="$work/g${grace}-${run}"
		mkdir -p "$dir"
		"$work/ckptctl" run --dir "$dir" --no-us=none --every=300ms >"$dir/controller.log" 2>&1 &
		ctl=$!
		sleep 1
		"$work/ckptctl" wrap --dir "$dir" --grace="${grace}s" --teardown=8s -- \
			"$work/ckpttrainer" --dir "$dir" --steps 100000 --step-ms 200 --write-ms "$write_ms" \
			--contract --grace "${grace}s" >"$dir/wrap.log" 2>&1 &
		wrap=$!
		sleep 3
		kill -TERM "$wrap"
		wait "$wrap" || true
		kill -TERM "$ctl" 2>/dev/null || true
		wait "$ctl" 2>/dev/null || true

		sealed=0
		grep -q "sealed at step" "$dir/wrap.log" && sealed=1 || true
		lost=0
		grep -q "final write NOT made" "$dir/wrap.log" && lost=1 || true
		echo "grace=${grace}s run=${run} sealed=${sealed} lost=${lost}"

		if [ "$grace" = 10 ] || [ "$grace" = 1 ]; then
			[ "$lost" = 1 ] || { echo "FAIL: grace ${grace} lost nothing"; bad=1; }
		else
			{ [ "$sealed" = 1 ] && [ "$lost" = 0 ]; } || { echo "FAIL: grace ${grace} did not seal"; bad=1; }
		fi
	done
done
exit "$bad"
