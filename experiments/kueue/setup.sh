#!/bin/sh
# Build the lab: kind, Kueue, and the queue the cases run against.
#
# Idempotent: running it twice does not create a second cluster, and a cluster that already has Kueue
# running is left alone.
set -eu

CLUSTER=${CLUSTER:-kueue-lab}
KUEUE_VERSION=${KUEUE_VERSION:-v0.9.0}
IMAGE="registry.k8s.io/kueue/kueue:${KUEUE_VERSION}"
KUBECONFIG_FILE="/tmp/kc-${CLUSTER}"
HERE=$(cd "$(dirname "$0")" && pwd)

say() { printf '\n== %s\n' "$1"; }

say "cluster"
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
	echo "cluster $CLUSTER already exists"
else
	kind create cluster --name "$CLUSTER" --wait 120s
fi

# WSL cannot reach the node's published port on 127.0.0.1; the container IP works.
say "kubeconfig (container IP, not 127.0.0.1)"
IP=$(docker inspect "${CLUSTER}-control-plane" -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
kind get kubeconfig --name "$CLUSTER" > "$KUBECONFIG_FILE"
sed -i "s#https://127.0.0.1:[0-9]*#https://${IP}:6443#" "$KUBECONFIG_FILE"
export KUBECONFIG="$KUBECONFIG_FILE"
kubectl get nodes

say "kueue $KUEUE_VERSION"
if kubectl -n kueue-system get deploy kueue-controller-manager >/dev/null 2>&1; then
	echo "kueue already installed"
else
	# The manifest is fetched through gh (which has egress) rather than by the docker daemon, which
	# does not.
	gh release download "$KUEUE_VERSION" -R kubernetes-sigs/kueue -p 'manifests.yaml' -O /tmp/kueue.yaml --clobber
	kubectl apply --server-side --validate=false -f /tmp/kueue.yaml >/dev/null

	# The image cannot be pulled from inside the cluster; crane can fetch it from here.
	if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
		mkdir -p /tmp/dockercfg
		printf '{"auths":{}}' > /tmp/dockercfg/config.json
		DOCKER_CONFIG=/tmp/dockercfg crane pull "$IMAGE" --format tarball /tmp/kueue.tar
		DOCKER_CONFIG=/tmp/dockercfg docker load -i /tmp/kueue.tar
	fi
	kind load image-archive /tmp/kueue.tar --name "$CLUSTER"

	# Never pull what the node already has, and drop the metrics sidecar from gcr.io.
	kubectl -n kueue-system patch deploy kueue-controller-manager --type=json \
		-p '[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"},{"op":"remove","path":"/spec/template/spec/containers/1"}]'
	kubectl -n kueue-system wait --for=condition=Available deploy/kueue-controller-manager --timeout=180s
fi

say "queue"
kubectl apply -f "$HERE/queue.yaml"
kubectl get clusterqueue,localqueue

say "ready"
echo "export KUBECONFIG=$KUBECONFIG_FILE"
