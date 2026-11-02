#!/bin/sh
# Sweep the things a preemption depends on, record what we predicted and what happened.
#
# Five holders at 500m occupy 2500m of a 4000m queue, so 1500m is free. Three arrival sizes cross the
# line: 1500m fits (nobody may be taken), 2000m is 500m short (one unit), 4000m is 2500m short (all five).
#
# Each case records two numbers that must agree:
#   predicted  what our rule says before anything happens — the observer reads the world, the rule is
#              `shortfall > 0 AND something higher waiting AND the queue may preempt`, and
#              N = ceil(shortfall / unitSize)
#   observed   what Kueue actually took
#
# The CSV is the artefact. `./report.py` turns it into the confusion matrix — the only number that says
# whether the rule is worth anything, and the thing to run every day.
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

# free_read reads what the queue can still admit *before* the arrival is applied.
#
# Reading demand from the suspended jobs after the fact does not work: Kueue admits a preemptible arrival
# within a second, the suspended set empties, and the shortfall reads zero for a case that took a unit.
# So the arithmetic is the product's own: shortfall = what the arrival asks for − what the queue has free.
free_read() {
	rm -rf "$WORLD"
	"$OBSERVER" --world-dir="$WORLD" --cluster=lab --units=all --once >/dev/null 2>&1 || true
	python3 - "$WORLD" <<'PY'
import json, os, sys
world = sys.argv[1]
try:
    s = json.load(open(os.path.join(world, "lab", "summary.json")))
except Exception:
    print("0,0")
    sys.exit(0)
qs = s.get("queues") or []
# The queue with the least room is the one an arrival will hit first.
best = min(qs, key=lambda q: q.get("freeMilli", 0)) if qs else None
print(f"{best['freeMilli'] if best else 0},{1 if (best and best.get('allowed')) else 0}")
PY
}

# milli turns a request like 2000m into 2000.
milli() {
	case $1 in
	*m) echo "${1%m}" ;;
	*) echo $((${1} * 1000)) ;;
	esac
}

one_case() {
	policy=$1; queue=$2; gap=$3; prio=$4; cpu=$5

	# Deleting a running holder waits out its grace period (busybox ignores SIGTERM), which costs more
	# than the measurement: delete without waiting and poll until the names are free.
	kubectl delete jobs --all --ignore-not-found --cascade=foreground --wait=false >/dev/null 2>&1 || true
	kubectl delete pods --all --ignore-not-found --grace-period=0 --force --wait=false >/dev/null 2>&1 || true
	j=0
	while [ "$j" -lt 15 ]; do
		[ -z "$(kubectl get jobs -o name 2>/dev/null)" ] && break
		sleep 1
		j=$((j + 1))
	done
	sed "s#kueue.x-k8s.io/queue-name: lab}#kueue.x-k8s.io/queue-name: ${queue}}#" \
		"$HERE/holders.yaml" | kubectl apply -f - >/dev/null
	i=0
	while [ "$i" -lt 25 ]; do
		[ "$(admitted)" -ge 5 ] && break
		sleep 2
		i=$((i + 1))
	done
	if [ "$(admitted)" -lt 5 ]; then
		echo "$policy,$gap,$cpu,ERROR,na,na,na,na,na,holders-not-admitted" >> "$OUT"
		return
	fi

	# The rule's verdict, taken before anything happens: the queue's headroom, the arrival's size, and
	# whether it outranks the holders.
	f=$(free_read)
	free=$(echo "$f" | cut -d, -f1)
	allowed=$(echo "$f" | cut -d, -f2)
	want=$(milli "$cpu")
	short=$((want - free))
	[ "$short" -lt 0 ] && short=0
	if [ "$prio" = lab-high ]; then waiting=1; else waiting=0; fi
	if [ "$short" -gt 0 ] && [ "$waiting" = 1 ] && [ "$allowed" = 1 ]; then
		pred=$(( (short + UNIT_MILLI - 1) / UNIT_MILLI ))
	else
		pred=0
	fi

	sed -e "s/__QUEUE__/$queue/" -e "s/__PRIORITY__/$prio/" -e "s/__CPU__/$cpu/" \
		"$HERE/arrival.tpl.yaml" | kubectl apply -f - >/dev/null

	sleep 14

	obs=$(taken)
	adm=$(kubectl get job arrival -o jsonpath='{.spec.suspend}' 2>/dev/null || echo "missing")
	if [ "$adm" = "false" ]; then adm_text=yes; else adm_text=no; fi
	took=$(who)

	echo "$policy,$gap,$cpu,$short,$waiting,$allowed,$pred,$obs,$took,$adm_text" >> "$OUT"
	echo "$policy gap=$gap cpu=$cpu: predicted=$pred observed=$obs admitted=$adm_text"
}

printf 'policy,gap,cpu,shortfall_m,waiting,allowed,predicted,observed,taken,admitted\n' > "$OUT"

for policy in allow never; do
	if [ "$policy" = allow ]; then queue=lab; else queue=lab-np; fi
	for gap in 0 100; do
		if [ "$gap" = 0 ]; then prio=lab-low; else prio=lab-high; fi
		for cpu in 1500m 2000m 4000m; do
			one_case "$policy" "$queue" "$gap" "$prio" "$cpu"
		done
	done
done

kubectl delete jobs --all --ignore-not-found --cascade=foreground --wait=false >/dev/null 2>&1 || true
echo
echo "results written to $OUT"
