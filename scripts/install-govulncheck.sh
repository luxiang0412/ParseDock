#!/usr/bin/env bash
# Install the pinned govulncheck (known-vulnerability scan in CI and locally).
#
#   scripts/install-govulncheck.sh
#
# The module version and its go.sum hash are pinned here and cross-checked against
# images.lock.json by scripts/verify_pins.py. `go mod download` verifies the module
# against the checksum database; we additionally compare the hash with the pinned one.
#
# Never use `go run golang.org/x/vuln/cmd/govulncheck@latest`: with GOTOOLCHAIN=auto the
# go command switches to whatever toolchain that module asks for, and the scan then
# reports on *that* standard library instead of the pinned one we ship (a false clean
# result). Run the installed binary with GOTOOLCHAIN=local and the pinned `go` on PATH.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="v1.9.0"
H1="h1:12VaqnI9kJfqwRoPptxoqsUhQtdfrSbdksT7X/KY/sc="
DEST="$ROOT/runtime/bin"
BIN="$DEST/govulncheck"
export GOTOOLCHAIN=local

if [[ -x "$BIN" ]] && "$BIN" -version 2>/dev/null | grep -q "govulncheck@${VERSION}"; then
  echo "govulncheck $VERSION already installed"
  exit 0
fi

# Outside the module, so the download can never touch go.mod/go.sum.
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
sum="$(cd "$tmp" && go mod download -json "golang.org/x/vuln@${VERSION}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["Sum"])')"
if [[ "$sum" != "$H1" ]]; then
  echo "golang.org/x/vuln@${VERSION}: checksum $sum != pinned $H1" >&2
  exit 1
fi
mkdir -p "$DEST"
GOBIN="$DEST" go install "golang.org/x/vuln/cmd/govulncheck@${VERSION}"
"$BIN" -version | head -3
