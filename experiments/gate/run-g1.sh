#!/bin/sh
# G1: the gate holds the arrival until the victim's checkpoint seals, then creates the job.
#
# The kind node has no extra mounts, so the world and the checkpoints live under /g1 *inside the
# node container*, and the pods reach them by hostPath. The observer and the gate run inside the node
# too, for the same reason: they need the same directory the pods write. On a real deployment the
# gate runs on the host with the world on a shared volume; this is a harness, not the deployment.
#
# Prerequisites: kind, docker, kubectl, the kueue-lab cluster from experiments/kueue/setup.sh.
set -eu
export PATH=/usr/lib/wsl/lib:/usr/local/bin:/usr/local/go/bin:/usr/bin:/bin
CLUSTER=${CLUSTER:-kueue-lab}
NODE="${CLUSTER}-control-plane"
ROOT=${ROOT:-/root/tmp/gate-exp}
HERE=$(cd "$(dirname "$0")" && pwd)
KC='KUBECONFIG=/etc/kubernetes/admin.conf'

echo "== build"
cd "$HERE/../.."
rm -rf "$ROOT"
mkdir -p "$ROOT/bin" "$ROOT/build"
CGO_ENABLED=0 go build -trimpath -o "$ROOT/bin/ckptctl" ./cmd/ckptctl
CGO_ENABLED=0 go build -trimpath -o "$ROOT/bin/ckpttrainer" ./cmd/ckpttrainer
(cd contrib/gate && CGO_ENABLED=0 go build -trimpath -o "$ROOT/bin/ckptgate" .)
(cd contrib/observer && CGO_ENABLED=0 go build -trimpath -o "$ROOT/bin/ckpt-observer" .)
cp "$ROOT/bin/ckptctl" "$ROOT/bin/ckpttrainer" "$ROOT/build/"
cp "$HERE/job/start.sh" "$HERE/job/Dockerfile" "$ROOT/build/"
docker build -q -t gate-job:local "$ROOT/build" >/dev/null
kind load docker-image gate-job:local --name "$CLUSTER" >/dev/null

echo "== stage the node"
# The pattern in brackets keeps the command line of this very shell from matching itself: pkill -f
# would kill the shell that is running it, and the script would stop with SIGTERM before starting.
docker exec "$NODE" sh -c 'ps -ef | awk "/[c]kpt-observer/ {print \$2}" | xargs -r kill 2>/dev/null || true'
docker exec "$NODE" sh -c 'rm -rf /g1; mkdir -p /g1/bin /g1/world /g1/ckpt /g1/manifests'
docker cp "$ROOT/bin/ckpt-observer" "$NODE:/g1/bin/"
docker cp "$ROOT/bin/ckptgate" "$NODE:/g1/bin/"
docker cp "$HERE/manifests/lab.yaml" "$NODE:/g1/manifests/lab.yaml"
docker cp "$HERE/manifests/arrival.yaml" "$NODE:/g1/manifests/arrival.yaml"
docker exec -d "$NODE" sh -c "$KC /g1/bin/ckpt-observer --world-dir=/g1/world --cluster=a --every=2s --units=all >/g1/observer.log 2>&1"

echo "== the victim and four holders"
docker exec "$NODE" sh -c "$KC kubectl delete job --all --ignore-not-found >/dev/null"
docker exec "$NODE" sh -c "$KC kubectl apply -f /g1/manifests/lab.yaml >/dev/null"
i=0
while [ $i -lt 40 ]; do
  if docker exec "$NODE" sh -c "$KC kubectl get pods -l job-name=victim -o jsonpath='{.items[0].status.phase}' 2>/dev/null" | grep -q Running; then break; fi
  sleep 2; i=$((i+1))
done
sleep 12
VICTIM=$(docker exec "$NODE" sh -c "$KC kubectl get pods -l job-name=victim -o jsonpath='{.items[0].metadata.name}'")
echo "victim=$VICTIM"

echo "== the gate"
docker exec "$NODE" sh -c "$KC /g1/bin/ckptgate submit --manifest=/g1/manifests/arrival.yaml --world=/g1/world --cluster=a --queue=lab --ckpt-root=/g1/ckpt --deadline=90s --kubeconfig=/etc/kubernetes/admin.conf" | tee /root/tmp/gate.out

echo "== what happened"
docker exec "$NODE" sh -c "$KC kubectl get jobs; kubectl get pods -o wide | head -8"
# Wait for the scheduler to act on the arrival, then read the victim's last step and the newest
# checkpoint's step. lost = steps trained after the seal and before the death.
i=0
while [ $i -lt 60 ]; do
  if docker exec "$NODE" sh -c "$KC kubectl get pod $VICTIM -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null" | grep -q 2; then break; fi
  sleep 1; i=$((i+1))
done
sleep 5
STEP=$(docker exec "$NODE" sh -c "cat /g1/ckpt/$VICTIM/.ckpt-governor/step 2>/dev/null" || true)
SEAL=$(docker exec "$NODE" sh -c "f=\$(find /g1/ckpt/$VICTIM -name manifest.json | tail -1); grep -o '\"step\": *[0-9]*' \"\$f\" | tail -1 | grep -o '[0-9]*'" || true)
WAITED=$(grep -o 'after [0-9.]*s' /root/tmp/gate.out | tail -1 | grep -o '[0-9.]*s' || true)
echo "step at death=$STEP · newest checkpoint step=$SEAL · gate waited ${WAITED}"
if [ -n "$STEP" ] && [ -n "$SEAL" ]; then
  echo "intruder waited ${WAITED} · victim lost $((STEP - SEAL)) steps"
fi
