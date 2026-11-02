#!/bin/sh
# The join, under a real Kueue: a decision, a write, and a verdict, on a volume that was actually evicted.
#
# The matrix proves the classifier and `experiments/kueue/run.sh` proves preemption. This is where they meet:
# a writer fills the queue and writes under the contract, an intruder with a higher priority takes it, and the
# volume is judged afterwards — by `ckptctl verify`, the thing that would tell a customer what they have.
#
#   CLUSTER=kueue-lab sh experiments/cluster/join/run.sh
#
# Everything it creates it deletes, including on failure: a cluster left behind makes the next experiment start
# with archaeology instead of an experiment.
set -eu

CLUSTER=${CLUSTER:-kueue-lab}
HERE=$(cd "$(dirname "$0")" && pwd)
export KUBECONFIG=${KUBECONFIG:-/tmp/kc-${CLUSTER}}
K="kubectl --context kind-${CLUSTER}"

say() { printf '\n== %s\n' "$1"; }

cleanup() {
	$K delete namespace join --ignore-not-found --wait=false >/dev/null 2>&1 || true
}
trap cleanup EXIT

say "reset"
cleanup
sleep 4

say "claim and queue"
$K apply -f "$HERE/join.yaml" >/dev/null

say "holders fill the quota"
i=1
while [ "$i" -le 4 ]; do
	sed "s/HOLDER_NAME/holder-$i/" "$HERE/holder.yaml" | $K apply -f - >/dev/null
	i=$((i + 1))
done
i=0
while [ "$i" -lt 40 ]; do
	n=$($K -n join get workloads --no-headers 2>/dev/null | grep -c 'True' || true)
	[ "${n:-0}" -ge 4 ] && break
	sleep 3
	i=$((i + 1))
done
printf 'holders admitted: %s (of 4)\n' "${n:-0}"
if [ "${n:-0}" -lt 4 ]; then
	printf 'the queue never filled, so nothing can be preempted\n' >&2
	$K -n join get workloads
	exit 1
fi

say "the writer starts and writes"
$K apply -f "$HERE/writer.yaml" >/dev/null
$K -n join wait --for=condition=Ready pod -l job-name=writer --timeout=90s >/dev/null 2>&1 || true
# Long enough for at least one seal under the contract, and for the governor to have seen the world twice.
sleep 20
$K -n join logs -l job-name=writer --tail=5 2>/dev/null || true

say "the intruder takes it"
start=$(date +%s)
$K apply -f "$HERE/intruder.yaml" >/dev/null
taken=""
i=0
while [ "$i" -lt 40 ]; do
	taken=$($K -n join get workloads --no-headers 2>/dev/null | grep '^job-writer' | grep 'False' | head -1 || true)
	[ -n "$taken" ] && break
	sleep 2
	i=$((i + 1))
done
end=$(date +%s)
if [ -z "$taken" ]; then
	say "the writer was not taken"
	$K -n join get workloads
	exit 1
fi
printf 'taken after %ss: %s\n' "$((end - start))" "$taken"

say "the writer's last words"
$K -n join logs -l job-name=writer --tail=8 2>/dev/null || true
$K -n join logs -l job-name=writer --tail=8 2>/dev/null || true

say "clearing the way for the volume"
$K -n join delete job writer intruder --ignore-not-found --cascade=background >/dev/null 2>&1 || true
i=0
while [ "$i" -lt 40 ]; do
	$K -n join get pods --no-headers 2>/dev/null | grep -q . || break
	sleep 2
	i=$((i + 1))
done

say "what the eviction left on disk"
$K -n join delete job verify --ignore-not-found >/dev/null 2>&1 || true
$K apply -f "$HERE/join.yaml" >/dev/null
$K -n join wait --for=condition=complete job/verify --timeout=120s >/dev/null 2>&1 || true
$K -n join logs job/verify

say "verdict"
# Counting "verified" in the text passes on "verified 0" — the first version of this check did exactly that and
# reported success on an empty volume. The number is what is asserted.
if ! $K -n join logs job/verify | grep -Eq 'generations [1-9][0-9]* · verified [1-9]'; then
	printf 'no verified generation survived the eviction\n' >&2
	exit 1
fi
printf 'the loop closed under a real eviction\n'
