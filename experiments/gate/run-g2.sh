#!/bin/sh
# G2: kill the gate while it holds; nothing is blocked, gc marks the record uncovered, and a direct
# submission after the kill preempts with no lead.
#
# Run after run-g1.sh: the node already has the binaries, the image and the manifests.
set -eu
export PATH=/usr/lib/wsl/lib:/usr/local/bin:/usr/local/go/bin:/usr/bin:/bin
CLUSTER=${CLUSTER:-kueue-lab}
NODE="${CLUSTER}-control-plane"
HERE=$(cd "$(dirname "$0")" && pwd)
KC='KUBECONFIG=/etc/kubernetes/admin.conf'

echo "== a clean victim"
docker exec "$NODE" sh -c "$KC kubectl delete job --all --ignore-not-found >/dev/null"
docker exec "$NODE" sh -c 'rm -rf /g1/ckpt /g1/world; mkdir -p /g1/ckpt /g1/world'
docker exec "$NODE" sh -c "$KC kubectl apply -f /g1/manifests/lab.yaml >/dev/null"
i=0
while [ $i -lt 40 ]; do
  if docker exec "$NODE" sh -c "$KC kubectl get pods -l job-name=victim -o jsonpath='{.items[0].status.phase}' 2>/dev/null" | grep -q Running; then break; fi
  sleep 2; i=$((i+1))
done
sleep 12
VICTIM=$(docker exec "$NODE" sh -c "$KC kubectl get pods -l job-name=victim -o jsonpath='{.items[0].metadata.name}'")
echo "victim=$VICTIM"

echo "== hold, then kill -9"
docker exec -d "$NODE" sh -c "$KC /g1/bin/ckptgate submit --manifest=/g1/manifests/arrival.yaml --world=/g1/world --cluster=a --queue=lab --ckpt-root=/g1/ckpt --deadline=15s --kubeconfig=/etc/kubernetes/admin.conf > /g1/g2-gate.log 2>&1"
i=0
while [ $i -lt 20 ]; do
  if docker exec "$NODE" sh -c 'ls /g1/world/a/reclaims/*.json >/dev/null 2>&1'; then break; fi
  sleep 1; i=$((i+1))
done
docker exec "$NODE" sh -c 'cat /g1/g2-gate.log; cat /g1/world/a/reclaims/*.json'
PIDS=$(docker exec "$NODE" sh -c 'ps -ef | grep "[c]kptgate submit" | awk "{print \$2}"')
for p in $PIDS; do docker exec "$NODE" sh -c "kill -9 $p || true"; done
sleep 1
docker exec "$NODE" sh -c 'ps -ef | grep "[c]kptgate submit" || echo gate-is-dead'

echo "== past the deadline: gc"
sleep 16
docker exec "$NODE" sh -c "$KC /g1/bin/ckptgate gc --world=/g1/world --cluster=a"
docker exec "$NODE" sh -c 'cat /g1/world/a/reclaims/*.json'

echo "== direct submission, no gate"
docker cp "$HERE/manifests/arrival-direct.yaml" "$NODE:/g1/manifests/"
docker exec "$NODE" sh -c "$KC kubectl apply -f /g1/manifests/arrival-direct.yaml" 2>/dev/null || true
i=0
while [ $i -lt 120 ]; do
  TS=$(docker exec "$NODE" sh -c "$KC kubectl get pod $VICTIM -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null" || true)
  if [ -n "$TS" ]; then break; fi
  sleep 0.5; i=$((i+1))
done
echo "victim deletionTimestamp=$TS"
sleep 8
echo "step at death=$(docker exec "$NODE" sh -c "cat /g1/ckpt/$VICTIM/.ckpt-governor/step 2>/dev/null" || true)"
docker exec "$NODE" sh -c "find /g1/ckpt/$VICTIM -name manifest.json -printf '%TY-%Tm-%TdT%TH:%TM:%TS %p\n' 2>/dev/null | tail -2"
