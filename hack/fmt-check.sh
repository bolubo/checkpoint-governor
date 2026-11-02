#!/bin/sh
# Fail when a file gofmt would change is checked in.
#
# The old gate ran `make fmt`, which rewrites the file and passes: a push could be green while the tree
# was unformatted, and the next person to run `make check` got a diff they did not make.
files=$(gofmt -l $(git ls-files '*.go') 2>/dev/null)
if [ -n "$files" ]; then
	printf '%s\n' "$files"
	echo "gofmt: run make fmt"
	exit 1
fi
echo "gofmt: clean"
