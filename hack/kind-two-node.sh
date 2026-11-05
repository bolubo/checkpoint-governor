#!/bin/sh
# Two real kind nodes sharing one host directory: controller on one node, job on the other.
#
# This is the cluster row of the closeout: it answers "does the shared-filesystem assumption survive
# two nodes", and "what remains when the job on node A disappears while the controller on node B is
# still watching". It does not promise a coordinated stop across nodes — that is the framework's and
# the launcher's (design-open-needs §2.1) — it checks that the copies that landed are still sound.
#
# Usage: sh hack/kind-two-node.sh   (needs docker, kind, kubectl; creates and deletes its own cluster)
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
GO=${GO:-go}
cluster=ckpt-closeout
image=ckpt-closeout:local
share=/root/tmp/kind-share
work=$(mktemp -d)

cleanup() {
	if [ "${KEEP:-0}" = 1 ]; then
		echo "kind: KEEP=1, leaving cluster $cluster and $share for inspection"
		rm -rf "$work"
		return
	fi
	kubectl --context "kind-$cluster" delete pod controller job --ignore-not-found >/dev/null 2>&1 || true
	kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
	docker image rm "$image" >/dev/null 2>&1 || true
	rm -rf "$work"
	rm -rf "$share"
}
trap cleanup EXIT INT TERM

command -v kind >/dev/null || { echo "kind-two-node: kind is required" >&2; exit 2; }
command -v kubectl >/dev/null || { echo "kind-two-node: kubectl is required" >&2; exit 2; }
docker info >/dev/null 2>&1 || { echo "kind-two-node: docker is not running" >&2; exit 2; }

mkdir -p "$share"
cat >"$work/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $cluster
nodes:
- role: control-plane
  extraMounts:
  - hostPath: $share
    containerPath: /ckpt
- role: worker
  extraMounts:
  - hostPath: $share
    containerPath: /ckpt
EOF

echo "kind: creating two-node cluster $cluster"
kind create cluster --config "$work/kind.yaml" >/dev/null

CGO_ENABLED=0 "$GO" build -o "$work/ckptctl" ./cmd/ckptctl
CGO_ENABLED=0 "$GO" build -o "$work/ckpttrainer" ./cmd/ckpttrainer
cat >"$work/Dockerfile" <<EOF
FROM scratch
COPY ckptctl ckpttrainer /
WORKDIR /ckpt
EOF
docker build -q -t "$image" "$work" >/dev/null
kind load docker-image --name "$cluster" "$image" >/dev/null

cat >"$work/pods.yaml" <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: controller
spec:
  nodeSelector:
    kubernetes.io/hostname: $cluster-control-plane
  tolerations:
  - key: node-role.kubernetes.io/control-plane
    operator: Exists
    effect: NoSchedule
  containers:
  - name: c
    image: $image
    imagePullPolicy: IfNotPresent
    command: ["/ckptctl", "run", "--dir", "/ckpt", "--no-us=none", "--window=5s", "--every=1s"]
    volumeMounts:
    - name: share
      mountPath: /ckpt
  volumes:
  - name: share
    hostPath:
      path: /ckpt
      type: Directory
---
apiVersion: v1
kind: Pod
metadata:
  name: job
spec:
  nodeSelector:
    kubernetes.io/hostname: $cluster-worker
  containers:
  - name: j
    image: $image
    imagePullPolicy: IfNotPresent
    command: ["/ckpttrainer", "--dir", "/ckpt", "--steps", "1000000", "--step-ms", "50", "--write-ms", "800", "--contract", "--grace", "10s"]
    volumeMounts:
    - name: share
      mountPath: /ckpt
  volumes:
  - name: share
    hostPath:
      path: /ckpt
      type: Directory
EOF

kubectl --context "kind-$cluster" apply -f "$work/pods.yaml" >/dev/null
if ! kubectl --context "kind-$cluster" wait --for=condition=Ready pod/controller pod/job --timeout=120s >/dev/null; then
	kubectl --context "kind-$cluster" get pods -o wide || true
	kubectl --context "kind-$cluster" describe pod job | tail -25 || true
	kubectl --context "kind-$cluster" logs job 2>&1 | tail -10 || true
	exit 1
fi

echo "kind: running 30s (controller on control-plane, job on worker, both mount $share)"
sleep 30

echo "--- job log (last 6 lines)"
kubectl --context "kind-$cluster" logs job --tail=6 2>/dev/null || true
echo "--- controller log (last 6 lines)"
kubectl --context "kind-$cluster" logs controller --tail=6 2>/dev/null || true

# The cross-node loss: delete the job pod without a signal. The controller on the other node keeps
# running; the copies on the shared volume are the evidence this test owns.
kubectl --context "kind-$cluster" delete pod job --grace-period=0 --force >/dev/null 2>&1 || true
sleep 5
kubectl --context "kind-$cluster" delete pod controller --ignore-not-found >/dev/null 2>&1 || true

sealed=$(ls -d "$share"/gen-* 2>/dev/null | while read -r g; do
	[ -f "$g/manifest.json" ] && echo "$g"
done | wc -l)
verified=0
if "$work/ckptctl" verify --dir "$share" --all >"$work/verify.log" 2>&1; then
	verified=1
fi
echo "kind: nodes 2 · shared $share · sealed $sealed · verified $verified"
[ "$sealed" -gt 0 ] && [ "$verified" = 1 ]
