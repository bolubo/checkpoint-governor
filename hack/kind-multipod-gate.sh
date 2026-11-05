#!/bin/sh
# Two-pod gate harness: a Job victim with two ranks writing one generation, and one hold.
#
# The victim is a batch/v1 Job with an indexed completion mode: pod 0 and pod 1 each write their
# shard (rank0.pt, rank1.pt) into one shared directory, meet at a file barrier, and rank 0 seals a
# manifest that names both. The gate holds the arrival once for the workload, releases only after the
# seal reads back whole, and the platform then preempts the victim.
#
# Everything lives under /g1 *inside* the kind node. The checkpoint root is a PVC backed by a
# hostPath PV: one node, so "shared" here means "both pods on that node see it". A real multi-node
# root is an RWX volume or a shared filesystem; this harness does not claim to have tested one.
#
# Usage: sh hack/kind-multipod-gate.sh   (needs docker, kind, kubectl and the kueue-lab cluster)
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
GO=${GO:-go}
CLUSTER=${CLUSTER:-kueue-lab}
NODE="${CLUSTER}-control-plane"
WORK=${WORK:-/root/tmp/gate-multipod}
KC='KUBECONFIG=/etc/kubernetes/admin.conf'

cleanup() {
	docker exec "$NODE" sh -c 'ps -ef | awk "/[c]kpt-observer/ {print \$2}" | xargs -r kill 2>/dev/null || true' || true
	docker exec "$NODE" sh -c "$KC kubectl delete job victim holder-a holder-b holder-c holder-d arrival --ignore-not-found >/dev/null 2>&1 || true" || true
	docker exec "$NODE" sh -c "$KC kubectl delete pvc ckpt-shared --ignore-not-found >/dev/null 2>&1 || true" || true
	docker exec "$NODE" sh -c "$KC kubectl delete pv ckpt-static --ignore-not-found >/dev/null 2>&1 || true" || true
	docker exec "$NODE" sh -c 'rm -rf /g1' || true
	docker image rm ckpt-victim:local >/dev/null 2>&1 || true
	rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

command -v docker >/dev/null || { echo "kind-multipod-gate: docker is required" >&2; exit 2; }
command -v kind >/dev/null || { echo "kind-multipod-gate: kind is required" >&2; exit 2; }
command -v kubectl >/dev/null || { echo "kind-multipod-gate: kubectl is required" >&2; exit 2; }
docker info >/dev/null 2>&1 || { echo "kind-multipod-gate: docker is not running" >&2; exit 2; }
docker exec "$NODE" true 2>/dev/null || { echo "kind-multipod-gate: the $CLUSTER node is not running" >&2; exit 2; }

echo "== build"
rm -rf "$WORK"
mkdir -p "$WORK/bin" "$WORK/build"
CGO_ENABLED=0 "$GO" build -trimpath -o "$WORK/bin/ckptctl" ./cmd/ckptctl
(cd contrib/gate && CGO_ENABLED=0 "$GO" build -trimpath -o "$WORK/bin/ckptgate" .)
(cd contrib/observer && CGO_ENABLED=0 "$GO" build -trimpath -o "$WORK/bin/ckpt-observer" .)
cp "$WORK/bin/ckptctl" "$WORK/build/"
cp "$root/hack/kind-multipod-victim.py" "$WORK/build/victim.py"
cp -r "$root/contrib/ckpt-policy/ckpt_policy" "$WORK/build/"
cat >"$WORK/build/Dockerfile" <<'EOF'
FROM python:3.11-slim
COPY ckptctl /opt/bin/ckptctl
COPY victim.py /opt/victim.py
COPY ckpt_policy /opt/ckpt_policy
ENV PYTHONPATH=/opt
EOF
docker build -q -t ckpt-victim:local "$WORK/build" >/dev/null
kind load docker-image ckpt-victim:local --name "$CLUSTER" >/dev/null

echo "== stage the node"
docker exec "$NODE" sh -c 'ps -ef | awk "/[c]kpt-observer/ {print \$2}" | xargs -r kill 2>/dev/null || true'
docker exec "$NODE" sh -c 'rm -rf /g1; mkdir -p /g1/bin /g1/world /g1/ckpt /g1/manifests'
docker cp "$WORK/bin/ckpt-observer" "$NODE:/g1/bin/"
docker cp "$WORK/bin/ckptgate" "$NODE:/g1/bin/"

cat >"$WORK/pv.yaml" <<'EOF'
apiVersion: v1
kind: PersistentVolume
metadata:
  name: ckpt-static
spec:
  capacity: {storage: 100Mi}
  accessModes: [ReadWriteMany]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: ""
  hostPath: {path: /g1/ckpt, type: Directory}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ckpt-shared
  namespace: default
spec:
  accessModes: [ReadWriteMany]
  storageClassName: ""
  resources: {requests: {storage: 100Mi}}
EOF

cat >"$WORK/lab.yaml" <<'EOF'
apiVersion: batch/v1
kind: Job
metadata:
  name: victim
  namespace: default
  labels: {kueue.x-k8s.io/queue-name: lab}
spec:
  suspend: true
  completions: 2
  parallelism: 2
  completionMode: Indexed
  backoffLimit: 0
  template:
    spec:
      priorityClassName: lab-victim
      restartPolicy: Never
      containers:
        - name: victim
          image: ckpt-victim:local
          imagePullPolicy: IfNotPresent
          command: ["python3", "/opt/victim.py"]
          env:
            - {name: HOME, value: /tmp}
            - {name: PYTHONUNBUFFERED, value: "1"}
            - {name: CKPTCTL_ENGINE, value: /opt/bin/ckptctl}
            - name: POD_NAME
              valueFrom: {fieldRef: {fieldPath: metadata.name}}
            - name: NAMESPACE
              valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
          resources:
            requests: {cpu: "1000m", memory: "64Mi"}
          volumeMounts:
            - {name: ckpt, mountPath: /ckpt}
            - {name: world, mountPath: /world}
      volumes:
        - name: ckpt
          persistentVolumeClaim: {claimName: ckpt-shared}
        - name: world
          hostPath: {path: /g1/world}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: holder-a
  namespace: default
  labels: {kueue.x-k8s.io/queue-name: lab}
spec:
  suspend: true
  backoffLimit: 0
  template:
    spec:
      priorityClassName: lab-low
      restartPolicy: Never
      containers:
        - name: holder
          image: busybox:1.36
          imagePullPolicy: IfNotPresent
          command: ["sh", "-c", "sleep 3600"]
          resources:
            requests: {cpu: "500m", memory: "64Mi"}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: holder-b
  namespace: default
  labels: {kueue.x-k8s.io/queue-name: lab}
spec:
  suspend: true
  backoffLimit: 0
  template:
    spec:
      priorityClassName: lab-low
      restartPolicy: Never
      containers:
        - name: holder
          image: busybox:1.36
          imagePullPolicy: IfNotPresent
          command: ["sh", "-c", "sleep 3600"]
          resources:
            requests: {cpu: "500m", memory: "64Mi"}
EOF

cat >"$WORK/arrival.yaml" <<'EOF'
apiVersion: batch/v1
kind: Job
metadata:
  name: arrival
  namespace: default
  labels: {kueue.x-k8s.io/queue-name: lab}
spec:
  suspend: true
  backoffLimit: 0
  template:
    spec:
      priorityClassName: lab-high
      restartPolicy: Never
      containers:
        - name: arrival
          image: busybox:1.36
          imagePullPolicy: IfNotPresent
          command: ["sh", "-c", "sleep 120"]
          resources:
            requests: {cpu: "2000m", memory: "64Mi"}
EOF

docker cp "$WORK/pv.yaml" "$NODE:/g1/manifests/pv.yaml"
docker cp "$WORK/lab.yaml" "$NODE:/g1/manifests/lab.yaml"
docker cp "$WORK/arrival.yaml" "$NODE:/g1/manifests/arrival.yaml"

docker exec -d "$NODE" sh -c "$KC /g1/bin/ckpt-observer --world-dir=/g1/world --cluster=a --every=2s --units=all >/g1/observer.log 2>&1"

echo "== the victim workload"
docker exec "$NODE" sh -c "$KC kubectl delete job --all --ignore-not-found >/dev/null 2>&1 || true"
docker exec "$NODE" sh -c "$KC kubectl delete pvc ckpt-shared --ignore-not-found >/dev/null 2>&1 || true"
docker exec "$NODE" sh -c "$KC kubectl delete pv ckpt-static --ignore-not-found >/dev/null 2>&1 || true"
docker exec "$NODE" sh -c "$KC kubectl apply -f /g1/manifests/pv.yaml >/dev/null"
docker exec "$NODE" sh -c "$KC kubectl apply -f /g1/manifests/lab.yaml >/dev/null"

i=0
while [ $i -lt 60 ]; do
	if [ "$(docker exec "$NODE" sh -c "$KC kubectl get pods -l job-name=victim -o jsonpath='{.items[*].status.phase}' 2>/dev/null" | grep -o Running | wc -l)" -eq 2 ]; then
		break
	fi
	sleep 2
	i=$((i + 1))
done
if [ "$i" -ge 60 ]; then
	docker exec "$NODE" sh -c "$KC kubectl get pods -o wide" || true
	docker exec "$NODE" sh -c "$KC kubectl describe job victim | tail -20" || true
	echo "kind-multipod-gate: the victim's two pods never ran" >&2
	exit 1
fi

# The gate finds the workload by the unit id the controller publishes. Wait for rank 0's engine to
# write it, otherwise the gate sees no governed workload and submits without a hold.
i=0
while [ $i -lt 60 ]; do
	if docker exec "$NODE" sh -c 'test -f /g1/ckpt/victim/.ckpt-governor/state.json' 2>/dev/null; then
		break
	fi
	sleep 1
	i=$((i + 1))
done
if [ "$i" -ge 60 ]; then
	echo "kind-multipod-gate: rank 0 never published its plan" >&2
	docker exec "$NODE" sh -c "$KC kubectl logs -l job-name=victim --tail=40" >&2 || true
	exit 1
fi

i=0
while [ $i -lt 60 ]; do
	if [ "$(docker exec "$NODE" sh -c 'grep -l "\"workload\": \"default/victim\"" /g1/world/a/units/*.json 2>/dev/null | wc -l')" -eq 2 ]; then
		break
	fi
	sleep 1
	i=$((i + 1))
done
if [ "$i" -ge 60 ]; then
	echo "kind-multipod-gate: the observer never recorded both victim pods" >&2
	docker exec "$NODE" sh -c 'cat /g1/observer.log | tail -20' >&2 || true
	exit 1
fi

echo "== the gate"
docker exec "$NODE" sh -c "$KC /g1/bin/ckptgate submit --manifest=/g1/manifests/arrival.yaml --world=/g1/world --cluster=a --queue=lab --ckpt-root=/g1/ckpt --deadline=90s --kubeconfig=/etc/kubernetes/admin.conf" | tee "$WORK/gate.out"

echo "== what happened"
i=0
while [ $i -lt 60 ]; do
	if docker exec "$NODE" sh -c "$KC kubectl get job arrival -o jsonpath='{.metadata.name}' 2>/dev/null" | grep -q arrival; then
		break
	fi
	sleep 1
	i=$((i + 1))
done
if [ "$i" -ge 60 ]; then
	echo "kind-multipod-gate: the arrival was never created" >&2
	cat "$WORK/gate.out" >&2
	exit 1
fi

i=0
while [ $i -lt 60 ]; do
	if docker exec "$NODE" sh -c "$KC kubectl get pods -l job-name=victim -o jsonpath='{.items[*].metadata.deletionTimestamp}' 2>/dev/null" | grep -q .; then
		break
	fi
	sleep 1
	i=$((i + 1))
done
sleep 2

STEP=$(docker exec "$NODE" sh -c 'cat /g1/ckpt/victim/.ckpt-governor/step 2>/dev/null' || true)
SEAL=$(docker exec "$NODE" sh -c 'grep -o "\"step\": *[0-9]*" /g1/ckpt/victim/gen-0001/manifest.json 2>/dev/null | tail -1 | grep -o "[0-9]*"' || true)
RANKS=$(docker exec "$NODE" sh -c 'ls /g1/ckpt/victim/gen-0001/rank*.pt 2>/dev/null | wc -l' || true)
HOLDS=$(docker exec "$NODE" sh -c 'ls /g1/world/a/reclaims 2>/dev/null | wc -l' || true)

if [ "$RANKS" -ne 2 ]; then
	echo "kind-multipod-gate: the seal names $RANKS rank shards, want 2" >&2
	exit 1
fi
if [ "$HOLDS" -ne 1 ]; then
	echo "kind-multipod-gate: $HOLDS hold records, want one per workload" >&2
	exit 1
fi
if [ -z "$STEP" ] || [ -z "$SEAL" ] || [ "$STEP" -ne "$SEAL" ]; then
	echo "kind-multipod-gate: step at death=$STEP, seal=$SEAL" >&2
	exit 1
fi

echo "sealed both ranks · arrival created · victim lost $((STEP - SEAL))"
