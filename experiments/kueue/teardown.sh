#!/bin/sh
# Delete the lab. Nothing here is meant to survive; a cluster left behind costs disk and confuses the
# next run.
set -eu
CLUSTER=${CLUSTER:-kueue-lab}
kind delete cluster --name "$CLUSTER"
rm -f "/tmp/kc-${CLUSTER}" /tmp/kueue.tar /tmp/kueue.yaml
