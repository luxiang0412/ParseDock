#!/usr/bin/env bash
# SDK and version-consistency checks (PR-10).
#
# * installs the Python package into a throwaway venv (PEP 668-safe),
# * runs the Python SDK end-to-end test plus the new CLI,
# * builds the generated Java client with Maven,
# * asserts one version across the binary, the OpenAPI document, both SDKs and the pom.
#
# Usage: scripts/sdk-check.sh [--no-java]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
BIN="${DOCPARSE_BIN:-runtime/bin/docparse}"
VENV=runtime/venv-sdk

# Always build the current sources unless the caller pinned an explicit binary: reusing a
# stale runtime/bin/docparse would validate the SDK against code that is not under review
# (or report version drift after a bump that was never compiled).
if [[ -z "${DOCPARSE_BIN:-}" ]]; then
  echo "building $BIN from the current sources"
  CGO_ENABLED=0 go build -trimpath -o "$BIN" ./cmd/docparse
elif [[ ! -x "$BIN" ]]; then
  echo "missing DOCPARSE_BIN=$BIN" >&2
  exit 1
fi

if [[ ! -x "$VENV/bin/python" ]]; then
  python3 -m venv "$VENV"
fi
"$VENV/bin/pip" -q install -e sdk/python

echo "== version consistency =="
python3 scripts/check_versions.py --bin "$BIN"

echo "== python sdk =="
# Test exactly the binary whose version was just checked, not a hard-coded default.
DOCPARSE_BIN="$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")" "$VENV/bin/python" sdk/python/test_sdk.py

echo "== python cli =="
"$VENV/bin/parsedock" --version
# The CLI smoke run is covered by test_cli in sdk/python/test_sdk.py; this only proves
# that the console script is installed and executable.

if [[ "${1:-}" != "--no-java" ]]; then
  echo "== java sdk =="
  command -v mvn >/dev/null || { echo "maven not installed" >&2; exit 1; }
  mvn -q -f sdk/java-generated/pom.xml -DskipTests package
  jar=$(ls sdk/java-generated/target/parsedock-client-*.jar | grep -v -e sources -e javadoc -e tests | head -1)
  # Avoid a pipeline under `pipefail`: unzip gets SIGPIPE when grep exits early.
  listing=runtime/sdk-java-listing.txt
  unzip -Z1 "$jar" > "$listing"
  grep -q "org/openapitools/client/ApiClient.class" "$listing"
  echo "java client built: $jar"
fi

echo "sdk-check: pass"
