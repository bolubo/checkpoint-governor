#!/bin/sh
# footprint — what this machine has spent on experiments, and whether it is over budget.
#
# The big spenders are invisible to `du`: docker volumes and images live in docker's own storage, and a
# directory walk finds megabytes while fifty gigabytes sit in volumes. So this measures docker first.
#
#   sh hack/footprint.sh          # report; exit 1 when over budget
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)

# The budgets. They are deliberately low: an experiment that needs more should say so out loud, and a number
# nobody remembers to raise is a budget that gets ignored. Over means "clean up or justify it in the PR".
MAX_VOLUMES_MB=61440   # 60 GiB of docker volumes
MAX_IMAGES_MB=20480    # 20 GiB of images
MAX_CLUSTERS=2
MAX_GOCACHE_MB=15360   # 15 GiB
MAX_TMP_MB=2048

over=0
say() { printf '%-22s %s\n' "$1" "$2"; }
flag() { printf '%-22s %s  <-- over budget (%s)\n' "$1" "$2" "$3"; over=$((over + 1)); }

mb() { echo $(( ${1:-0} / 1024 / 1024 )); }

# --- docker: the part du cannot see ---------------------------------------------------------------
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
	vols_mb=$(docker system df --format '{{.Type}} {{.Size}}' 2>/dev/null | awk '$1=="Local"{print $2}' | head -1)
	vols_mb=$(echo "${vols_mb:-0B}" | sed 's/GB//' | awk '{ if ($0 ~ /GB/) printf "%d", $1*1024; else printf "%d", $1 }')
	imgs_mb=$(docker system df --format '{{.Type}} {{.Size}}' 2>/dev/null | awk '$1=="Images"{print $2}' | head -1)
	imgs_mb=$(echo "${imgs_mb:-0B}" | sed 's/GB//' | awk '{ if ($0 ~ /GB/) printf "%d", $1*1024; else printf "%d", $1 }')

	if [ "${vols_mb:-0}" -gt "$MAX_VOLUMES_MB" ]; then flag "docker volumes" "${vols_mb} MiB" "max ${MAX_VOLUMES_MB}"; else say "docker volumes" "${vols_mb} MiB"; fi
	if [ "${imgs_mb:-0}" -gt "$MAX_IMAGES_MB" ]; then flag "docker images" "${imgs_mb} MiB" "max ${MAX_IMAGES_MB}"; else say "docker images" "${imgs_mb} MiB"; fi

	reclaim=$(docker system df --format '{{.Type}} {{.Reclaimable}}' 2>/dev/null | awk '$1=="Images"{print $2}')
	say "images reclaimable" "${reclaim:-n/a}"
else
	say "docker" "not running"
fi

# --- kind clusters: each one holds a control plane and its volumes ---------------------------------
if command -v kind >/dev/null 2>&1; then
	n=$(kind get clusters 2>/dev/null | grep -c . || true)
	if [ "${n:-0}" -gt "$MAX_CLUSTERS" ]; then
		flag "kind clusters" "$n" "max ${MAX_CLUSTERS}"
		kind get clusters 2>/dev/null | sed 's/^/                       - /'
	else
		say "kind clusters" "$n"
	fi
fi

# --- caches and scratch --------------------------------------------------------------------------
if [ -d "${HOME:-/root}/go/pkg/mod" ]; then
	mb_go=$(du -sm "${HOME:-/root}/go" 2>/dev/null | awk '{print $1}')
	if [ "${mb_go:-0}" -gt "$MAX_GOCACHE_MB" ]; then flag "go cache+mod" "${mb_go} MiB" "max ${MAX_GOCACHE_MB}"; else say "go cache+mod" "${mb_go} MiB"; fi
fi

tmp_mb=$(du -sm /tmp 2>/dev/null | awk '{print $1}')
if [ "${tmp_mb:-0}" -gt "$MAX_TMP_MB" ]; then flag "/tmp" "${tmp_mb} MiB" "max ${MAX_TMP_MB}"; else say "/tmp" "${tmp_mb} MiB"; fi

say "repo worktrees" "$(du -sm "$root" 2>/dev/null | awk '{print $1}') MiB"

printf '\n'
if [ "$over" -gt 0 ]; then
	printf '%d over budget. The reclaim commands, in the order that reclaims most:\n' "$over"
	printf '  docker volume prune          # only unattached: check `kind get clusters` first\n'
	printf '  docker image prune -a        # keep the base images you rebuild from\n'
	printf '  kind delete cluster --name X # one per unused cluster\n'
	printf '  go clean -cache\n'
	exit 1
fi
printf 'within budget\n'
