#!/usr/bin/env bash
# Install the pinned actionlint binary (workflow linting in CI and locally).
#
#   scripts/install-actionlint.sh
#
# Version and checksum are pinned here and cross-checked against images.lock.json
# by scripts/verify_pins.py, so a runner can never lint with a different version.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="1.7.7"
SHA256="023070a287cd8cccd71515fedc843f1985bf96c436b7effaecce67290e7e0757"
DEST="$ROOT/runtime/bin"
BIN="$DEST/actionlint"

if [[ -x "$BIN" ]] && "$BIN" -version 2>/dev/null | grep -q "$VERSION"; then
  echo "actionlint $VERSION already installed"
  exit 0
fi

mkdir -p "$DEST"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
url="https://github.com/rhysd/actionlint/releases/download/v${VERSION}/actionlint_${VERSION}_linux_amd64.tar.gz"
curl -fsSL -o "$tmp/actionlint.tgz" "$url"
echo "${SHA256}  $tmp/actionlint.tgz" | sha256sum -c - >/dev/null
tar -xzf "$tmp/actionlint.tgz" -C "$DEST" actionlint
chmod +x "$BIN"
"$BIN" -version
