#!/bin/sh
# G1, five times, one build: the distribution of the gate's wait and the victim's loss.
#
# Same shapes as run-g1.sh, but the image and the observer are set up once and the cluster is
# reset between runs. The world is not cleaned between runs: the observer keeps publishing, and
# only the checkpoints and the jobs are reset.
set -eu
export PATH=/usr/lib/wsl/lib:/usr/local/bin:/usr/local/go/bin:/usr/bin:/bin
CLUSTER=${CLUSTER:-kueue-lab}
NODE="${CLUSTER}-control-plane"
ROOT=${ROOT:-/root/tmp/gate-exp}
HERE=$(cd "$(dirname "$0")" && pwd)
KC='KUBECONFIG=/etc/kubernetes/admin.conf'
cd "$HERE/../.."

echo "== build once"
rm -rf "$ROOT"; mkdir -p "$ROOT/bin" "$ROOT/build"
CGO_ENABLED=0 go build -trimpath -o "$ROOT/bin/ckptctl" ./cmd/ckptctl
CGO_ENABLED=0 go build -trimpath -o "$ROOT/bin/ckpttrainer" ./cmd/ckpttrainer
(cd contrib/gate && CGO_ENABLED=0 go build -trimpath -o "$ROOT/bin/ckptgate" .)
(cd contrib/observer && CGO_ENABLED=0 go build -trimpath -o "$ROOT/bin/ckpt-observer" .)
cp "$ROOT/bin/ckptctl" "$ROOT/bin/ckpttrainer" "$ROOT/build/"
cp "$HERE/job/start.sh" "$HERE/job/Dockerfile" "$ROOT/build/"
docker build -q -t gate-job:local "$ROOT/build" >/dev/null
kind load docker-image gate-job:local --name "$CLUSTER" >/dev/null

echo "== stage once"
docker exec "$NODE" sh -c 'ps -ef | awk "/[c]kpt-observer/ {print \$2}" | xargs -r kill 2>/dev/null || true'
docker exec "$NODE" sh -c 'rm -rf /g1; mkdir -p /g1/bin /g1/world /g1/ckpt /g1/manifests'
docker cp "$ROOT/bin/ckpt-observer" "$NODE:/g1/bin/"
docker cp "$ROOT/bin/ckptgate" "$NODE:/g1/bin/"
docker cp "$HERE/manifests/lab.yaml" "$NODE:/g1/manifests/lab.yaml"
docker cp "$HERE/manifests/arrival.yaml" "$NODE:/g1/manifests/arrival.yaml"
docker exec -d "$NODE" sh -c "$KC /g1/bin/ckpt-observer --world-dir=/g1/world --cluster=a --every=2s --units=all >/g1/observer.log 2>&1"
sleep 3

printf 'run  waited  step  seal  lost\n'
for run in 1 2 3 4 5; do
	docker exec "$NODE" sh -c "$KC kubectl delete job --all --ignore-not-found >/dev/null"
	docker exec "$NODE" sh -c 'rm -rf /g1/ckpt; mkdir -p /g1/ckpt'
	docker exec "$NODE" sh -c "$KC kubectl apply -f /g1/manifests/lab.yaml >/dev/null"
	i=0
	while [ $i -lt 40 ]; do
		if docker exec "$NODE" sh -c "$KC kubectl get pods -l job-name=victim -o jsonpath='{.items[0].status.phase}' 2>/dev/null" | grep -q Running; then break; fi
		sleep 2; i=$((i+1))
	done
	sleep 12
	VICTIM=$(docker exec "$NODE" sh -c "$KC kubectl get pods -l job-name=victim -o jsonpath='{.items[0].metadata.name}'")
	docker exec "$NODE" sh -c "$KC /g1/bin/ckptgate submit --manifest=/g1/manifests/arrival.yaml --world=/g1/world --cluster=a --queue=lab --ckpt-root=/g1/ckpt --deadline=90s --kubeconfig=/etc/kubernetes/admin.conf" > "$ROOT/g1.out" 2>&1
	WAITED=$(grep -o 'after [0-9.]*s' "$ROOT/g1.out" | tail -1 | grep -o '[0-9.]*' || true)
	if [ -z "$WAITED" ]; then
		echo "$run  no-seal"
		cat "$ROOT/g1.out"
		continue
	fi
	i=0
	while [ $i -lt 60 ]; do
		if docker exec "$NODE" sh -c "$KC kubectl get pod $VICTIM -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null" | grep -q 2; then break; fi
		sleep 1; i=$((i+1))
	done
	sleep 4
	STEP=$(docker exec "$NODE" sh -c "cat /g1/ckpt/$VICTIM/.ckpt-governor/step 2>/dev/null" || true)
	SEAL=$(docker exec "$NODE" sh -c "f=\$(find /g1/ckpt/$VICTIM -name manifest.json | tail -1); grep -o '\"step\": *[0-9]*' \"\$f\" | tail -1 | grep -o '[0-9]*'" || true)
	LOST=$((STEP - SEAL))
	printf '%s  %s  %s  %s  %s\n' "$run" "$WAITED" "$STEP" "$SEAL" "$LOST"
done
