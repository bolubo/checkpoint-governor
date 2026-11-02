#!/bin/sh
# Build the Python wheel, with the Go engine inside it.
#
# This is the whole install: `pip install` puts the client and the engine in one place, so there is no
# second download, no PATH entry and no `go install` (decision 0028, P1.1). The binary is package data,
# built for this machine; wheels for other machines are the release workflow's job (P1.4).
set -eu

HERE=$(cd "$(dirname "$0")/.." && pwd)
PKG="$HERE/contrib/ckpt-policy"
BIN="$PKG/ckpt_policy/_bin"
OUT="$HERE/dist/wheels"
GO=${GO:-go}

mkdir -p "$BIN" "$OUT"
rm -f "$BIN"/ckptctl "$BIN"/ckptctl.exe

GOOS=$(cd "$HERE" && "$GO" env GOOS)
GOARCH=$(cd "$HERE" && "$GO" env GOARCH)
EXT=""
if [ "$GOOS" = "windows" ]; then EXT=".exe"; fi
echo "engine: $GOOS/$GOARCH"

(cd "$HERE" && "$GO" build -trimpath -o "$BIN/ckptctl$EXT" ./cmd/ckptctl)

"$BIN/ckptctl$EXT" version

# `pip wheel` rather than `python -m build`: the latter is a build dependency, and the point is that a
# laptop with pip can produce the wheel.
python3 -m pip wheel "$PKG" -w "$OUT" --no-deps -q

WHEEL=$(ls -t "$OUT"/ckpt_policy-*.whl 2>/dev/null | head -1)
if [ -z "$WHEEL" ]; then
	echo "wheel: not built" >&2
	exit 1
fi

# The wheel is only useful if the engine is actually inside it, so the script says so instead of
# trusting the packaging configuration.
if python3 - "$WHEEL" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1]) as z:
    names = z.namelist()
if not any(n.endswith("_bin/ckptctl") or n.endswith("_bin/ckptctl.exe") for n in names):
    sys.exit("the wheel does not contain the engine")
PY
then
	echo "wheel: $(basename "$WHEEL"), engine inside"
else
	echo "wheel: engine missing" >&2
	exit 1
fi

cat <<EOF

install:  python3 -m pip install "$WHEEL"
EOF
