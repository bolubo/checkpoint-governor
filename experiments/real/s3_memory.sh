#!/bin/sh
# S3: host.memory-pressure in a real cgroup.
#
# The probe reads /sys/fs/cgroup/memory.current and memory.max of its own cgroup. A host process is
# not in a limited cgroup (the root is "max"), so the condition is absent there. A container with
# --memory=2g gives the probe exactly the two files it reads, and the memhog in the same container
# drives current to the 95% line. The container limit is the safety: the host is never the victim.
#
#   TRIALS=3 sh experiments/real/s3_memory.sh
#
# The condition is persistent, not an event. The first write lands; the next attempt a few steps
# later is refused by the 60s cooldown, and the copy stays where it was. That is the number this
# script reports, not a death.
set -eu
cd "$(dirname "$0")/../.."
ROOT=${ROOT:-/root/tmp/s3mem}
TRIALS=${TRIALS:-3}
GO=${GO:-go}
DOCKER=${DOCKER:-docker}
EVERY=${EVERY:-1s}
STEP_MS=${STEP_MS:-20}
WRITE_MS=${WRITE_MS:-40}
SHARD=${SHARD:-1048576}
LIMIT=${LIMIT:-2g}
HOG=${HOG:-1940}

mkdir -p "$ROOT/bin" "$ROOT/build"
CGO_ENABLED=0 "$GO" build -trimpath -o "$ROOT/bin/ckptctl" ./cmd/ckptctl
CGO_ENABLED=0 "$GO" build -trimpath -o "$ROOT/bin/ckpttrainer" ./cmd/ckpttrainer
CGO_ENABLED=0 "$GO" build -trimpath -o "$ROOT/bin/memhog" ./experiments/real/memhog.go
cp "$ROOT/bin/ckptctl" "$ROOT/bin/ckpttrainer" "$ROOT/bin/memhog" "$ROOT/build/"
cat > "$ROOT/build/start.sh" <<EOF
#!/bin/sh
set -eu
mkdir -p /ckpt
/usr/local/bin/ckptctl run --dir=/ckpt --unit=mem --no-us=none --every=$EVERY > /ckpt/ctl.log 2>&1 &
/usr/local/bin/ckpttrainer --dir=/ckpt --steps=1000000 --step-ms=$STEP_MS --write-ms=$WRITE_MS --shard-bytes=$SHARD --ranks=2 > /ckpt/train.log 2>&1 &
sleep 2
/usr/local/bin/memhog $HOG > /ckpt/mem.log 2>&1 &
wait
EOF
chmod +x "$ROOT/build/start.sh"
cat > "$ROOT/build/Dockerfile" <<'EOF'
FROM busybox:1.36
COPY ckptctl ckpttrainer memhog /usr/local/bin/
COPY start.sh /start.sh
RUN chmod 0755 /start.sh
ENTRYPOINT ["/start.sh"]
EOF
"$DOCKER" build -q -t memjob:local "$ROOT/build" >/dev/null

printf 'run  current/limit  oom  action/why  step  seal  lost\n'
for i in $(seq 1 "$TRIALS"); do
	dir="$ROOT/job-$i"
	rm -rf "$dir"; mkdir -p "$dir"
	name="s3mem-$i"
	"$DOCKER" rm -f "$name" >/dev/null 2>&1 || true
	"$DOCKER" run -d --name "$name" --memory="$LIMIT" --memory-swap="$LIMIT" -v "$dir:/ckpt" memjob:local >/dev/null
	j=0
	while [ "$j" -lt 55 ]; do
		if [ -f "$dir/.ckpt-governor/state.json" ] && grep -q '"action": "save"' "$dir/.ckpt-governor/state.json"; then break; fi
		if find "$dir" -name manifest.json -print -quit 2>/dev/null | grep -q .; then break; fi
		sleep 1; j=$((j + 1))
	done
	sizes=$("$DOCKER" exec "$name" sh -c 'cat /sys/fs/cgroup/memory.current; cat /sys/fs/cgroup/memory.max' 2>/dev/null || true)
	cur=$(printf '%s\n' "$sizes" | sed -n 1p)
	lim=$(printf '%s\n' "$sizes" | sed -n 2p)
	oom=$("$DOCKER" exec "$name" sh -c 'awk "/oom_kill /{print \$2}" /sys/fs/cgroup/memory.events' 2>/dev/null || echo -)
	action=$(grep -o '"action": "[^"]*"' "$dir/.ckpt-governor/state.json" 2>/dev/null | head -1 | cut -d'"' -f4)
	why=$(grep -o '"why": "[^"]*"' "$dir/.ckpt-governor/state.json" 2>/dev/null | head -1 | cut -d'"' -f4)
	step=$(cat "$dir/.ckpt-governor/step" 2>/dev/null || echo 0)
	seal=$(find "$dir" -name manifest.json -printf '%p\n' 2>/dev/null | tail -1 | sed 's#.*/step-##; s#/manifest.json##')
	[ -z "$seal" ] && seal=0
	lost=$((step - seal))
	printf '%s  %s/%s  %s  %s/%s  %s  %s  %s\n' "$i" "$cur" "$lim" "$oom" "$action" "$why" "$step" "$seal" "$lost"
	"$DOCKER" stop "$name" >/dev/null 2>&1 || true
done
