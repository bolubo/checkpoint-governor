#!/bin/sh
set -eu
cd "$(dirname "$0")/../.."
ROOT=${ROOT:-/root/tmp/s3end}
TRIALS=${TRIALS:-3}
GO=${GO:-go}
mkdir -p "$ROOT/bin"
CGO_ENABLED=0 "$GO" build -trimpath -o "$ROOT/bin/ckptctl" ./cmd/ckptctl
CGO_ENABLED=0 "$GO" build -trimpath -o "$ROOT/bin/ckpttrainer" ./cmd/ckpttrainer

printf 'run  may-exit  step  seal  lost  why\n'
for i in $(seq 1 "$TRIALS"); do
	dir="$ROOT/job-$i"
	rm -rf "$dir"
	"$ROOT/bin/ckptctl" run --dir="$dir" --no-us=none --every=1s >"$dir.ctl.log" 2>&1 &
	ctl=$!
	"$ROOT/bin/ckpttrainer" --dir="$dir" --steps 50 --step-ms 20 --write-ms 40 \
		--shard-bytes 1048576 --ranks 2 >"$dir.train.log" 2>&1 || true
	j=0
	while [ "$j" -lt 30 ]; do
		[ -f "$dir/.ckpt-governor/may-exit" ] && break
		sleep 1; j=$((j + 1))
	done
	kill "$ctl" 2>/dev/null || true
	wait "$ctl" 2>/dev/null || true
	step=$(cat "$dir/.ckpt-governor/step" 2>/dev/null || echo 0)
	seal=$(find "$dir" -name manifest.json -printf '%p\n' 2>/dev/null | tail -1 | sed 's#.*/step-##; s#/manifest.json##')
	[ -z "$seal" ] && seal=0
	lost=$((step - seal))
	why=$(grep -o '"why": "[^"]*"' "$dir/.ckpt-governor/state.json" 2>/dev/null | head -1 | cut -d'"' -f4)
	me=$(test -f "$dir/.ckpt-governor/may-exit" && echo yes || echo no)
	printf '%s  %s  %s  %s  %s  %s\n' "$i" "$me" "$step" "$seal" "$lost" "$why"
done
