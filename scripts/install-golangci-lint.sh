#!/usr/bin/env bash
# Install the pinned golangci-lint binary (Go linting in CI and locally).
#
#   scripts/install-golangci-lint.sh
#
# Version and checksum are pinned here and cross-checked against images.lock.json
# by scripts/verify_pins.py, so a runner can never lint with a different version.
# golangci-lint is a CI-only tool (GPL-3.0); it is never shipped in an image or linked.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="2.15.0"
SHA256="9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea"
DEST="$ROOT/runtime/bin"
BIN="$DEST/golangci-lint"

if [[ -x "$BIN" ]] && "$BIN" version 2>/dev/null | grep -q "version $VERSION "; then
  echo "golangci-lint $VERSION already installed"
  exit 0
fi

mkdir -p "$DEST"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
name="golangci-lint-${VERSION}-linux-amd64"
url="https://github.com/golangci/golangci-lint/releases/download/v${VERSION}/${name}.tar.gz"
curl -fsSL -o "$tmp/golangci-lint.tgz" "$url"
echo "${SHA256}  $tmp/golangci-lint.tgz" | sha256sum -c - >/dev/null
tar -xzf "$tmp/golangci-lint.tgz" -C "$tmp" "${name}/golangci-lint"
install -m 0755 "$tmp/${name}/golangci-lint" "$BIN"
"$BIN" version
