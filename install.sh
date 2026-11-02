#!/usr/bin/env sh
# Install ckptctl and ckpttrainer from a release tarball.
#
#   curl -fsSL https://raw.githubusercontent.com/bolubo/checkpoint-governor/main/install.sh | sh
#
# No Go, no container, no package manager. The tarball's checksum is verified before anything is
# installed; an unverified binary is not installed at all.
#
# Environment:
#   PREFIX                 where to install (default: $HOME/.local/bin)
#   VERSION                a tag like v0.15.0 (default: the latest release)
#   CKPTCTL_RELEASE_BASE   where the tarball and SHA256SUMS live; a URL base (file:// works, which is
#                          how CI proves this script offline against a freshly built dist/)
set -eu

REPO="bolubo/checkpoint-governor"
PREFIX="${PREFIX:-$HOME/.local/bin}"
VERSION="${VERSION:-latest}"
BASE="${CKPTCTL_RELEASE_BASE:-}"

say() { printf '%s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "unsupported architecture: $arch (releases are linux/amd64 and linux/arm64 only)" ;;
esac
case "$os" in
	linux) ;;
	*) die "unsupported operating system: $os (releases are linux only; use 'go install' from a checkout)" ;;
esac

if [ "$VERSION" = "latest" ]; then
	if [ -n "$BASE" ]; then
		die "VERSION is required when CKPTCTL_RELEASE_BASE is set"
	fi
	VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
		grep -m1 '"tag_name"' | cut -d'"' -f4)
	[ -n "$VERSION" ] || die "cannot find the latest release; pass VERSION=vX.Y.Z"
else
	[ -n "$BASE" ] || BASE="https://github.com/$REPO/releases/download/$VERSION"
fi
[ -n "$BASE" ] || die "no download base for $VERSION"

name="checkpoint-governor_${VERSION}_linux_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

say "fetching $name from $BASE"
curl -fsSL "$BASE/$name" -o "$tmp/$name" || die "cannot download $name from $BASE"
curl -fsSL "$BASE/SHA256SUMS" -o "$tmp/SHA256SUMS" || die "cannot download SHA256SUMS from $BASE"

grep " $name\$" "$tmp/SHA256SUMS" >"$tmp/wanted" || die "SHA256SUMS does not list $name"
(cd "$tmp" && sha256sum -c wanted) || die "checksum mismatch for $name; nothing was installed"

tar -xzf "$tmp/$name" -C "$tmp"
mkdir -p "$PREFIX"
# The tarball holds the binaries with the architecture in their names; the installed names are the
# ones a person types.
mv "$tmp/ckptctl_linux_${arch}" "$PREFIX/ckptctl"
mv "$tmp/ckpttrainer_linux_${arch}" "$PREFIX/ckpttrainer"
chmod 0755 "$PREFIX/ckptctl" "$PREFIX/ckpttrainer"

say "installed $( "$PREFIX/ckptctl" version) → $PREFIX"
case ":$PATH:" in
	*":$PREFIX:"*) ;;
	*) say "note: $PREFIX is not on PATH; add it, or run $PREFIX/ckptctl" ;;
esac
