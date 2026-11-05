#!/bin/sh
# The version-skew matrix with real binaries: an old reader meets a declaration from a newer build.
#
# A hand-written error string proves only that the test can quote itself. This checks out the ref that
# shipped the previous reader, builds it, and lets it refuse the declaration on its own terms — then
# hands the same file to this build, which must accept it.
#
# Usage: sh hack/skew.sh [OLD_REF]   (default v0.24.0; falls back to the v0.24 merge commit)
#        OLD_REF=some-ref sh hack/skew.sh
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
GO=${GO:-go}

old_ref=${1:-${OLD_REF:-v0.24.0}}
if ! git rev-parse --verify "${old_ref}^{commit}" >/dev/null 2>&1; then
	for fallback in v0.24-one-pass 7c9d246; do
		if git rev-parse --verify "${fallback}^{commit}" >/dev/null 2>&1; then
			echo "old ref: $old_ref not found, using $fallback"
			old_ref=$fallback
			break
		fi
	done
fi
if ! git rev-parse --verify "${old_ref}^{commit}" >/dev/null 2>&1; then
	echo "FAIL: no usable old ref; pass one, for example OLD_REF=v0.24-one-pass" >&2
	exit 1
fi
resolved=$(git rev-parse --short "${old_ref}^{commit}")
echo "old ref: $old_ref ($resolved)"

work=$(mktemp -d)
cleanup() {
	git worktree remove --force "$work/old" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT INT TERM

git worktree add --detach "$work/old" "${old_ref}" >/dev/null
(cd "$work/old" && "$GO" build -o "$work/old-ckptctl" ./cmd/ckptctl)
"$GO" build -o "$work/new-ckptctl" ./cmd/ckptctl

# A copy our own writer sealed, with a declaration that names the reader it needs. The fields are the
# ones both builds know, so the only difference between the two readers is the version they judge.
job="$work/job"
mkdir -p "$job/gen-1"
printf '{}\n' >"$job/gen-1/manifest.json"
cat >"$job/gen-1/resume.json" <<'EOF'
{
  "schema": 1,
  "generation": 1,
  "step": 1,
  "format": "other",
  "world": {"ranks": 1, "tp": 0, "pp": 0, "dp": 0, "reshardable": false},
  "state": {"model": true, "optimizer": true, "scheduler": true, "rng": true, "dataloader": true, "scaler": false, "ema": false},
  "min_reader": "0.25"
}
EOF

old_out=$("$work/old-ckptctl" resumable --dir "$job" 2>&1 || true)
printf '%s\n' "$old_out"
case "$old_out" in
*"needs ckptctl >= 0.25 (you have 0.24)"*)
	echo "old reader refused: needs ckptctl >= 0.25 (you have 0.24)"
	;;
*)
	echo "FAIL: the old reader did not refuse the upgrade" >&2
	exit 1
	;;
esac

new_out=$("$work/new-ckptctl" resumable --dir "$job" 2>&1 || true)
printf '%s\n' "$new_out"
case "$new_out" in
*"resumable"*)
	echo "new reader accepted"
	;;
*)
	echo "FAIL: this build refused a declaration it can read" >&2
	exit 1
	;;
esac
