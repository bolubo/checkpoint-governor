#!/bin/sh
# release — push the tag, and let the workflow publish.
#
# This exists because doing it by hand is wrong in a way that looks fine: `gh release create v0.21.0` creates the
# tag, which triggers `.github/workflows/release.yml`, which then tries to create the same release and dies with
# "a release with the same tag name already exists". The release stays published with **no binaries and no
# checksums** — a page a stranger can find, download nothing from, and a red run nobody looks at afterwards.
#
#   sh hack/release.sh v0.21.0
#
# The order is the whole point: the CHANGELOG section first (the workflow takes its notes from it), then the tag
# on that commit, then wait for the pipeline.
set -eu

tag=${1:?usage: sh hack/release.sh v0.21.0}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

if ! grep -q "^## ${tag} " CHANGELOG.md; then
	printf 'no CHANGELOG section for %s: the release would fall back to generated notes\n' "$tag" >&2
	exit 2
fi

if [ -n "$(git status --porcelain)" ]; then
	printf 'the tree is dirty; a release is tagged on a commit, not on a working directory\n' >&2
	exit 2
fi

git fetch --quiet origin main
if [ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]; then
	printf 'HEAD is not origin/main; tag the commit that is actually released\n' >&2
	exit 2
fi

printf 'tagging %s and pushing: the workflow builds the binaries, the image and the release\n' "$tag"
git tag -a "$tag" -m "$tag"
git push origin "$tag"

printf 'watching the release workflow\n'
sleep 5
run=$(gh run list --workflow release --limit 1 --json databaseId -q '.[0].databaseId')
gh run watch "$run" --exit-status
printf 'published: %s\n' "$(gh release view "$tag" --json url -q .url)"
