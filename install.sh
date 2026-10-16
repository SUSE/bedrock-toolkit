#!/bin/sh
# Installs bedrock and/or bedrock-admin from the GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/SUSE/bedrock-toolkit/main/install.sh | sh -s -- bedrock
#
# Arguments: bedrock, bedrock-admin, or both (default: bedrock).
# Environment: VERSION (a tag such as v1.2.0; default: the latest release),
# INSTALL_DIR (default: /usr/local/bin when writable, else ~/.local/bin).
# Needs only sh, curl, tar and sha256sum (or shasum).
set -eu

REPO=SUSE/bedrock-toolkit

die() { echo "install.sh: $*" >&2; exit 1; }

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) die "unsupported OS $(uname -s): only macOS and Linux have builds" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported CPU $(uname -m): only amd64 and arm64 have builds" ;;
esac

[ $# -gt 0 ] || set -- bedrock
for c in "$@"; do
  case "$c" in
    bedrock | bedrock-admin) ;;
    *) die "unknown CLI '$c': use bedrock or bedrock-admin" ;;
  esac
done

if [ -z "${INSTALL_DIR:-}" ]; then
  if [ -w /usr/local/bin ]; then INSTALL_DIR=/usr/local/bin; else INSTALL_DIR="$HOME/.local/bin"; fi
fi
mkdir -p "$INSTALL_DIR"

if command -v sha256sum >/dev/null 2>&1; then sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else die "need sha256sum or shasum to check the download"; fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

command -v curl >/dev/null 2>&1 || die "need curl"
tag=${VERSION:-$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" | sed 's|.*/||')}
case "$tag" in v*) ;; *) die "can't find the latest release of $REPO" ;; esac
fetch() { curl -fsSL -o "$tmp/$1" "https://github.com/$REPO/releases/download/$tag/$1"; }

version=${tag#v}
fetch checksums.txt || die "can't download $tag from $REPO"
for c in "$@"; do
  archive="${c}_${version}_${os}_${arch}.tar.gz"
  fetch "$archive" || die "can't download $archive"
  want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
  [ -n "$want" ] && [ "$want" = "$(sha "$tmp/$archive")" ] || die "checksum mismatch for $archive"
  tar -xzf "$tmp/$archive" -C "$tmp" "$c"
  install -m 0755 "$tmp/$c" "$INSTALL_DIR/$c"
  echo "Installed $("$INSTALL_DIR/$c" --version) to $INSTALL_DIR/$c"
done

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Add $INSTALL_DIR to your PATH." ;;
esac
