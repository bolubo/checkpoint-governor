#!/bin/sh
# Checks the claim table: every promise in docs/ names a test that exists.
#
# The table is the answer to "we said it works — where does that failure show up?" A row with no test
# is a sentence nobody checks; a test that does not exist is a row that was never true.
#
# Usage: sh hack/claims.sh [table]
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
table=${1:-hack/claims.tsv}

claims=0
found=0
missing=0
while IFS='|' read -r claim where test; do
	case "$claim" in
	"" | "#"*) continue ;;
	esac
	claims=$((claims + 1))
	test=$(printf '%s' "$test" | tr -d ' ')
	if [ -z "$test" ]; then
		echo "missing: ${claim} names no test"
		missing=$((missing + 1))
		continue
	fi
	case "$test" in
	py:*)
		name=${test#py:}
		class=${name%%.*}
		method=${name#*.}
		if grep -q "def ${method}(" contrib/ckpt-policy/tests/test_governor.py &&
			grep -q "class ${class}(" contrib/ckpt-policy/tests/test_governor.py; then
			found=$((found + 1))
		else
			echo "missing: ${claim} -> ${test}"
			missing=$((missing + 1))
		fi
		;;
	*)
		if grep -rq "^func ${test}(" --include="*_test.go" .; then
			found=$((found + 1))
		else
			echo "missing: ${claim} -> ${test}"
			missing=$((missing + 1))
		fi
		;;
	esac
done <"$table"

echo "claims: ${claims} · tests found: ${found} · missing: ${missing}"
[ "$missing" -eq 0 ]
