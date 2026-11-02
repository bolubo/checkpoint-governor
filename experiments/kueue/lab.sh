#!/bin/sh
# Rebuild what lives outside the cluster: the kubeconfig that reaches the node, and the observer.
#
# Both are written under /tmp, which this machine clears when WSL restarts. Run this whenever the
# cluster answers but kubectl does not, or before a sweep in a fresh session.
set -eu

CLUSTER=${CLUSTER:-kueue-lab}
HERE=$(cd "$(dirname "$0")" && pwd)
KC=/tmp/kc-${CLUSTER}

# WSL cannot reach the node's published port on 127.0.0.1; the container IP works.
IP=$(docker inspect "${CLUSTER}-control-plane" -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
kind get kubeconfig --name "$CLUSTER" > "$KC"
sed -i "s#https://127.0.0.1:[0-9]*#https://${IP}:6443#" "$KC"
echo "kubeconfig: $KC (server https://${IP}:6443)"

(cd "$HERE/../../contrib/observer" && go build -o /tmp/obs .)
echo "observer: /tmp/obs"

KUBECONFIG=$KC kubectl get nodes --request-timeout=10s 2>&1 | tail -2
