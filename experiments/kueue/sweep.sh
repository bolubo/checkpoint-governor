#!/bin/sh
# Sweep the things a preemption depends on and record what actually happens.
#
# Five holders at 500m occupy 2500m of a 4000m queue, so 1500m is free. The arrival's size against that
# number is the only thing that should decide whether somebody dies:
#
#   500m / 1500m  -> fits,      nobody may be taken
#   2000m         -> 500m short, one unit
#   3000m         -> 1500m short, three units
#   4000m         -> 2500m short, five units
#
# Two priority gaps (equal, and one level up) and two queue policies (preemption allowed, Never). The
# Never rows must all read zero; if they do not, the signal "a higher priority is waiting" is not the
# one we think it is.
#
# Output is one CSV row per case on stdout, so the table is the artefact rather than a paragraph about
# it. Each case resets the cluster: a preemption deletes pods, and a re-run on a dirty queue measures
# the previous case.
set -eu

CLUSTER=${CLUSTER:-kueue-lab}
HERE=$(cd "$(dirname "$0")" && pwd)
export KUBECONFIG=${KUBECONFIG:-/tmp/kc-${CLUSTER}}
OUT=${OUT:-$HERE/results.csv}

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

one_case() {
	policy=$1; queue=$2; gap=$3; prio=$4; cpu=$5; short=$6

	kubectl delete jobs --all --ignore-not-found >/dev/null 2>&1 || true
	kubectl delete pods --all --ignore-not-found >/dev/null 2>&1 || true

	sed "s#kueue.x-k8s.io/queue-name: lab}#kueue.x-k8s.io/queue-name: ${queue}}#" \
		"$HERE/holders.yaml" | kubectl apply -f - >/dev/null
	i=0
	while [ "$i" -lt 25 ]; do
		[ "$(admitted)" -ge 5 ] && break
		sleep 2
		i=$((i + 1))
	done
	if [ "$(admitted)" -lt 5 ]; then
		echo "$policy,$gap,$cpu,$short,ERROR,holders-not-admitted,na" >> "$OUT"
		return
	fi

	sed -e "s/__QUEUE__/$queue/" -e "s/__PRIORITY__/$prio/" -e "s/__CPU__/$cpu/" \
		"$HERE/arrival.tpl.yaml" | kubectl apply -f - >/dev/null

	sleep 18

	n=$(taken)
	adm=$(kubectl get job arrival -o jsonpath='{.spec.suspend}' 2>/dev/null || echo "missing")
	if [ "$adm" = "false" ]; then admitted_text=yes; else admitted_text=no; fi

	echo "$policy,$gap,$cpu,$short,$n,$(who),$admitted_text" >> "$OUT"
	echo "$policy gap=$gap cpu=$cpu short=$short -> taken=$n admitted=$admitted_text $(who)"
}

printf 'policy,gap,cpu,shortfall_m,preempted,taken,arrival_admitted\n' > "$OUT"

for policy in allow never; do
	if [ "$policy" = allow ]; then queue=lab; else queue=lab-np; fi
	for gap in 0 100; do
		if [ "$gap" = 0 ]; then prio=lab-low; else prio=lab-high; fi
		# 1500m is the control that proves the sweep can read "fits"; 500m measures the same thing.
		for cpu in 1500m 2000m 4000m; do
			case $cpu in
			1500m) short=0 ;;
			2000m) short=500 ;;
			3000m) short=1500 ;;
			4000m) short=2500 ;;
			esac
			one_case "$policy" "$queue" "$gap" "$prio" "$cpu" "$short"
		done
	done
done

kubectl delete jobs --all --ignore-not-found >/dev/null 2>&1 || true
echo
echo "results written to $OUT"
