#!/bin/sh
# The victim's entrypoint: one controller and one trainer over one shared directory.
#
# The unit id is the pod name, which is what the observer writes; the controller publishes it in
# state.json so the gate can find this directory by identity rather than by guessing a path.
set -eu
DIR="/ckpt/$(hostname)"
mkdir -p "$DIR/.ckpt-governor"
cat > "$DIR/.ckpt-governor/deployment.json" <<EOF
{"schema":1,"world":{"dir":"/g1/world"},"capabilities":{"preemption":{"exists":true},"eviction":{"exists":true}}}
EOF
/usr/local/bin/ckptctl run --dir="$DIR" --unit="default/$(hostname)" --no-us=none --every=2s &
exec /usr/local/bin/ckpttrainer --dir="$DIR" --contract --steps=1000000 \
  --step-ms=2000 --write-ms=1500 --shard-bytes=4194304 --grace=0s
