#!/usr/bin/env sh
# Print the CHANGELOG section for one released version, for the tag workflow's release notes.
#
#   sh hack/release-notes.sh v0.15.0
set -eu
version="${1:?usage: release-notes.sh <version>}"
awk -v want="## ${version}" '
	/^## / {
		if (found) exit
		if ($0 ~ want) { found = 1; next }
	}
	found { print }
' "$(dirname "$0")/../CHANGELOG.md"
