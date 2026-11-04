#!/bin/sh
# The end-to-end proof for multi-rank glue: two real processes write their own steps, one controller
# reads them, and `status` says which ranks reported.
#
# Usage: sh hack/members.sh   (needs go and python3 on PATH; a few seconds)
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
GO=${GO:-go}
cd "$root"
$GO build -o "$work/ckptctl" ./cmd/ckptctl
export PYTHONPATH="$root/contrib/ckpt-policy:${PYTHONPATH:-}"

dir="$work/job"
mkdir -p "$dir"
"$work/ckptctl" run --dir "$dir" --no-us=none --every=200ms >"$work/controller.log" 2>&1 &
ctl=$!
sleep 1

python3 - "$dir" <<'PY' &
import sys, time
from ckpt_policy import Governor
g = Governor(sys.argv[1], rank=0)
for i in range(100):
    g.step(i)
    time.sleep(0.01)
PY
lead=$!
python3 - "$dir" <<'PY' &
import sys, time
from ckpt_policy import Governor
g = Governor(sys.argv[1], rank=1)
for i in range(96):
    g.step(i)
    time.sleep(0.01)
PY
follower=$!
wait "$lead" "$follower"
sleep 1
kill -TERM "$ctl" 2>/dev/null || true
wait "$ctl" 2>/dev/null || true

line=$("$work/ckptctl" status "$dir" | grep "^members")
echo "status: $line"
case "$line" in
"members         2/2 · spread 4"*) echo "PASS: two ranks, the slowest one is the unit" ;;
*) echo "FAIL: expected 'members 2/2 · spread 4'"; exit 1 ;;
esac
