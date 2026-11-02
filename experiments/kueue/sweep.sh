#!/bin/sh
# Sweep the things a preemption depends on, record what we predicted and what happened.
#
# Five holders at 500m occupy 2500m of a 4000m queue, so 1500m is free unless a case says otherwise.
# Each row records two numbers that must agree:
#   predicted  the rule's answer *before* anything happens — headroom, the arrival's declared size, and
#              whether it outranks the holders; N = ceil(shortfall / unit)
#   observed   what Kueue actually took
#
# The base grid is the measured one. The extra cases are the three ways it could be wrong:
#   lower    same priority, queue policy LowerOrNewerEqualPriority — our rule says "cannot happen"
#   double   two arrivals at once — N is no longer one arrival's size
#   cohort   borrowing — quota stops being a hard boundary, so a shortfall stops being a prediction
set -eu

CLUSTER=${CLUSTER:-kueue-lab}
HERE=$(cd "$(dirname "$0")" && pwd)
export KUBECONFIG=${KUBECONFIG:-/tmp/kc-${CLUSTER}}
OUT=${OUT:-$HERE/results.csv}
OBSERVER=${OBSERVER:-/tmp/obs}
WORLD=${WORLD:-/tmp/sweep-world}
UNIT_MILLI=500

admitted() {
	n=0
	for j in 1 2 3 4 5; do
		s=$(kubectl get job "holder-$j" -o jsonpath='{.spec.suspend}' 2>/dev/null || echo "")
		[ "$s" = "false" ] && n=$((n + 1))
	done
	echo "$n"
}

taken() {
	n=0
	for j in 1 2 3 4 5; do
		s=$(kubectl get job "holder-$j" -o jsonpath='{.spec.suspend}' 2>/dev/null || echo "")
		[ "$s" = "true" ] && n=$((n + 1))
	done
	echo "$n"
}

who() {
	out=""
	for j in 1 2 3 4 5; do
		s=$(kubectl get job "holder-$j" -o jsonpath='{.spec.suspend}' 2>/dev/null || echo "")
		[ "$s" = "true" ] && out="${out}holder-$j;"
	done
	echo "${out:-none}"
}

# free_read reads what the queue can still admit *before* the arrival is applied: reading demand after
# the fact reports zero, because Kueue admits a preemptible arrival within about a second.
free_read() {
	q=$1
	rm -rf "$WORLD"
	"$OBSERVER" --world-dir="$WORLD" --cluster=lab --units=all --once >/dev/null 2>&1 || true
	python3 - "$WORLD" "$q" <<'PY'
import json, os, sys
world, want = sys.argv[1], sys.argv[2]
try:
    s = json.load(open(os.path.join(world, "lab", "summary.json")))
except Exception:
    print("0,0")
    sys.exit(0)
for q in s.get("queues") or []:
    if q.get("name") == want:
        # A cohort lends its members' unused room, so the headroom an arrival sees is the group's —
        # but only where the policy permits borrowing, so the capacity comes along too.
        free = q.get("cohortFreeMilli") or q.get("freeMilli", 0)
        print(f"{free},{1 if q.get('allowed') else 0},{q.get('capacityMilli', 0)}")
        sys.exit(0)
# No fallback: a queue the summary does not name is a queue we cannot read, and guessing one is how a
# Never queue once predicted five preemptions that could not happen.
print("0,0")
PY
}

# cq_of maps a LocalQueue name to the ClusterQueue the summary is keyed by. One line, because the lab
# names them almost identically and the one exception is worth stating rather than deriving.
cq_of() {
	case $1 in
	lab-np) echo lab-nopreempt ;;
	*) echo "$1" ;;
	esac
}

milli() {
	case $1 in
	*m) echo "${1%m}" ;;
	*) echo $((${1} * 1000)) ;;
	esac
}

# one_case policy queue prio cpu [holders_queue] [second_arrival]
one_case() {
	policy=$1; queue=$2; prio=$3; cpu=$4; hq=${5:-$2}; second=${6:-no}

	kubectl delete jobs --all --ignore-not-found --cascade=foreground --wait=false >/dev/null 2>&1 || true
	kubectl delete pods --all --ignore-not-found --grace-period=0 --force --wait=false >/dev/null 2>&1 || true
	j=0
	while [ "$j" -lt 15 ]; do
		[ -z "$(kubectl get jobs -o name 2>/dev/null)" ] && break
		sleep 1
		j=$((j + 1))
	done

	sed "s#kueue.x-k8s.io/queue-name: lab}#kueue.x-k8s.io/queue-name: ${hq}}#" \
		"$HERE/holders.yaml" | kubectl apply -f - >/dev/null
	i=0
	while [ "$i" -lt 25 ]; do
		[ "$(admitted)" -ge 5 ] && break
		sleep 2
		i=$((i + 1))
	done
	if [ "$(admitted)" -lt 5 ]; then
		echo "$policy,$cpu,ERROR,na,na,na,na,na" >> "$OUT"
		return
	fi

	# The verdict, taken before anything happens.
	f=$(free_read "$(cq_of "$queue")")
	free=$(echo "$f" | cut -d, -f1)
	allowed=$(echo "$f" | cut -d, -f2)
	cap=$(echo "$f" | cut -d, -f3)
	want=$(milli "$cpu")
	[ "$second" = yes ] && want=$((want * 2))
	short=$((want - free))
	[ "$short" -lt 0 ] && short=0
	# Past the queue's capacity the arrival is never admitted, so nobody is taken for it.
	if [ "${cap:-0}" -gt 0 ] && [ "$want" -gt "$cap" ]; then short=0; fi
	if [ "$prio" = lab-high ]; then waiting=1; else waiting=0; fi
	if [ "$short" -gt 0 ] && [ "$waiting" = 1 ] && [ "$allowed" = 1 ]; then
		pred=$(( (short + UNIT_MILLI - 1) / UNIT_MILLI ))
	else
		pred=0
	fi

	sed -e "s/__QUEUE__/$queue/" -e "s/__PRIORITY__/$prio/" -e "s/__CPU__/$cpu/" \
		"$HERE/arrival.tpl.yaml" | kubectl apply -f - >/dev/null
	if [ "$second" = yes ]; then
		sed -e "s/__QUEUE__/$queue/" -e "s/__PRIORITY__/$prio/" -e "s/__CPU__/$cpu/" \
			-e "s/name: arrival/name: arrival-b/" "$HERE/arrival.tpl.yaml" | kubectl apply -f - >/dev/null
	fi

	sleep 14

	obs=$(taken)
	took=$(who)
	echo "$policy,$cpu,$short,$waiting,$allowed,$pred,$obs,$took" >> "$OUT"
	echo "$policy cpu=$cpu: predicted=$pred observed=$obs"
}

printf 'policy,cpu,shortfall_m,waiting,allowed,predicted,observed,taken\n' > "$OUT"

# The measured base grid.
for policy in allow never; do
	if [ "$policy" = allow ]; then queue=lab; else queue=lab-np; fi
	for prio in lab-low lab-high; do
		# A fresh label each round: appending to `policy` accumulated until the names read
		# `allow-gap0-gap0-gap0-gap100` and nothing could be matched by eye.
		if [ "$prio" = lab-high ]; then label="$policy-gap100"; else label="$policy-gap0"; fi
		for cpu in 1500m 2000m 4000m; do
			one_case "$label" "$queue" "$prio" "$cpu"
		done
	done
done

# Same priority on a queue that preempts newer-or-equal work. Our rule says this cannot happen.
for cpu in 2000m 4000m; do
	one_case lower-equal lab-lower lab-low "$cpu"
done

# Two arrivals in the same round: the demand is the sum, and N stops being one arrival's size.
for cpu in 1000m 2000m; do
	one_case double lab lab-high "$cpu" lab yes
done

# Borrowing: the arrival lands in a 500m queue inside a 4-core cohort and has to borrow from the other.
for cpu in 2000m 3000m; do
	one_case cohort lab-cohort-b lab-high "$cpu" lab-cohort-a
done

kubectl delete jobs --all --ignore-not-found --cascade=foreground --wait=false >/dev/null 2>&1 || true
echo
echo "results written to $OUT"
