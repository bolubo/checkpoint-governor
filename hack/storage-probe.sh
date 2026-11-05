#!/bin/sh
# Measure what one directory's filesystem promises, on the machine that has the mount.
#
# Run this on a host that can see the checkpoint volume — an NFS/Lustre client, a node with the PVC
# mounted, or the submitter's laptop. It writes a 4 KiB file, syncs it, renames it over another file,
# syncs the directory, and reads the result from a second process. The output is the same table
# `ckptctl doctor --storage` prints, and it is what `docs/storage.md` means by local / shared-safe /
# shared-unsafe.
#
# Usage: sh hack/storage-probe.sh <checkpoint-dir>
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
dir=${1:-}
if [ -z "$dir" ]; then
	echo "usage: sh hack/storage-probe.sh <checkpoint-dir>" >&2
	exit 2
fi
if [ ! -d "$dir" ]; then
	echo "storage-probe: $dir is not a directory" >&2
	exit 2
fi

GO=${GO:-go}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

CGO_ENABLED=0 "$GO" build -o "$work/ckptctl" "$root/cmd/ckptctl"
echo "storage-probe: $dir"
"$work/ckptctl" doctor --storage "$dir"
