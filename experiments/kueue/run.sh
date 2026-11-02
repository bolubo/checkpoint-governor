#!/bin/sh
# Run one case and print its numbers.
#
# The measurement is ground truth only: how many holders Kueue actually takes, and which. The number
# that matters is `preempted` — for c0 and c5 it must be 0, for c1 it should be 1.
#
# Two things this script learned the hard way, both recorded because they change the result:
#
# 1. Kueue preempts by suspending the workload again, so the job returns to suspend=true while its pod
#    is still terminating. Counting pod phases reads 0 for a case that did preempt.
# 2. A case is only about preemption if the holders and the arrival share one ClusterQueue. Two
#    queues have two quotas, and "nothing was preempted" then means "nothing competed", which proves
#    the opposite of what the case claims. Hence QUEUE below.
set -eu

CASE=${1:-c0}
CLUSTER=${CLUSTER:-kueue-lab}
HERE=$(cd "$(dirname "$0")" && pwd)
export KUBECONFIG=${KUBECONFIG:-/tmp/kc-${CLUSTER}}

say() { printf '\n== %s\n' "$1"; }

admitted_holders() {
	n=0
	for j in 1 2 3 4 5; do
		s=$(kubectl get job "holder-$j" -o jsonpath='{.spec.suspend}' 2>/dev/null || echo "")
		[ "$s" = "false" ] && n=$((n + 1))
	done
	echo "$n"
}

# The queue the holders join. c5 must use the queue that may not preempt, or the case measures a
# second, idle quota instead of a refusal.
case "$CASE" in
c5) QUEUE=lab-np ;;
c0 | c1) QUEUE=lab ;;
*)
	echo "unknown case $CASE (expected c0, c1 or c5)"
	exit 2
	;;
esac

say "reset"
kubectl delete jobs --all --ignore-not-found >/dev/null 2>&1 || true
kubectl delete pods --all --ignore-not-found >/dev/null 2>&1 || true

say "holders on queue $QUEUE"
sed "s#kueue.x-k8s.io/queue-name: lab}#kueue.x-k8s.io/queue-name: ${QUEUE}}#" \
	"$HERE/holders.yaml" | kubectl apply -f - >/dev/null
i=0
while [ "$i" -lt 30 ]; do
	[ "$(admitted_holders)" -ge 5 ] && break
	sleep 2
	i=$((i + 1))
done
before=$(admitted_holders)
echo "holders admitted: $before"
if [ "$before" -lt 5 ]; then
	echo "holders were not admitted; the case cannot be judged"
	kubectl get jobs
	kubectl get workloads
	exit 1
fi

case "$CASE" in
c0) say "c0: nothing arrives" ;;
c1)
	say "c1: one 2000m arrival, preemption allowed, same queue"
	kubectl apply -f "$HERE/c1-arrival.yaml" >/dev/null
	;;
c5)
	say "c5: one 2000m arrival, preemption Never, same queue"
	kubectl apply -f "$HERE/c5-arrival.yaml" >/dev/null
	;;
esac

sleep 25

say "result"
preempted=0
for j in 1 2 3 4 5; do
	s=$(kubectl get job "holder-$j" -o jsonpath='{.spec.suspend}' 2>/dev/null || echo "")
	if [ "$s" = "true" ]; then
		preempted=$((preempted + 1))
		echo "  taken: holder-$j"
	fi
done
echo "preempted: $preempted"
echo
echo "--- jobs (suspended means Kueue took its quota away) ---"
kubectl get jobs -o custom-columns='NAME:.metadata.name,SUSPEND:.spec.suspend,ACTIVE:.status.active' 2>/dev/null || true
echo
echo "--- workloads ---"
kubectl get workloads -o custom-columns='NAME:.metadata.name,ADMITTED:.status.conditions[?(@.type=="Admitted")].status' 2>/dev/null || true
echo
echo "--- why the arrival is waiting, if it is ---"
kubectl get workloads -o jsonpath='{range .items[*]}{.metadata.name}: {range .status.conditions[*]}{.type}={.status} {.message} {end}{"\n"}{end}' 2>/dev/null | grep arrival || true
