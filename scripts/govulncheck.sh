#!/usr/bin/env bash
# Known-vulnerability scan of ParseDock with the pinned Go toolchain.
#
#   scripts/govulncheck.sh [extra govulncheck flags]
#
# govulncheck reports standard-library vulnerabilities for the toolchain it runs with,
# so the scan is only meaningful when that toolchain is the one images.lock.json pins
# (the one the shipped binary is built with). GOTOOLCHAIN=local forbids a silent switch
# and the version check refuses to scan with anything else.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export GOTOOLCHAIN=local
want="go$(python3 -c 'import json; print(json.load(open("images.lock.json"))["go"])')"
have="$(go env GOVERSION)"
if [[ "$have" != "$want" ]]; then
  echo "govulncheck: go on PATH is $have, images.lock.json pins $want; refusing to scan a different stdlib" >&2
  exit 1
fi
bash "$ROOT/scripts/install-govulncheck.sh" >/dev/null
exec "$ROOT/runtime/bin/govulncheck" "$@" ./cmd/... ./internal/... ./migrations/...
