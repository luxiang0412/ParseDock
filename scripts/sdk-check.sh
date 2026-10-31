#!/usr/bin/env bash
# SDK and version-consistency checks.
#
# Three layers, all against a real ParseDock process (never a mock HTTP server):
#
#   1. python lifecycle  sdk/python/test_sdk.py         create→wait→download→ack, CLI
#   2. python matrix     sdk/python/test_sdk_matrix.py  error codes, retry policy, SSE resume
#   3. real engines      sdk/python/test_sdk_real.py    native+xberg output through the SDK
#   4. java client       mvn generated client + sdk/java-client-test integration test
#
# Usage:
#   scripts/sdk-check.sh            # everything (nightly)
#   scripts/sdk-check.sh --fast     # PR gate: layers 1-2 + version consistency, no Java
#   scripts/sdk-check.sh --no-java  # skip the Java build/test
#   scripts/sdk-check.sh --no-real  # skip the real-engine layer (no engines installed)
#
# The real-engine layer skips itself (exit 0) when the xberg binary is absent, so
# --fast can run where engines are not installed.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
BIN="${DOCPARSE_BIN:-runtime/bin/docparse}"
VENV=runtime/venv-sdk
FAST=0
WITH_JAVA=1
WITH_REAL=1
for arg in "$@"; do
  case "$arg" in
    --fast) FAST=1; WITH_JAVA=0; WITH_REAL=0 ;;
    --no-java) WITH_JAVA=0 ;;
    --no-real) WITH_REAL=0 ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done
if [[ $FAST -eq 0 ]]; then WITH_REAL=${WITH_REAL:-1}; fi

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
BIN="$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")"

if [[ ! -x "$VENV/bin/python" ]]; then
  python3 -m venv "$VENV"
fi
"$VENV/bin/pip" -q install -e sdk/python

echo "== version consistency =="
python3 scripts/check_versions.py --bin "$BIN"

echo "== python sdk: lifecycle =="
DOCPARSE_BIN="$BIN" "$VENV/bin/python" sdk/python/test_sdk.py

echo "== python sdk: error matrix =="
DOCPARSE_BIN="$BIN" "$VENV/bin/python" sdk/python/test_sdk_matrix.py

if [[ $WITH_REAL -eq 1 ]]; then
  echo "== python sdk: real engines =="
  DOCPARSE_BIN="$BIN" "$VENV/bin/python" sdk/python/test_sdk_real.py
else
  echo "== python sdk: real engines (skipped: --fast/--no-real) =="
fi

if [[ $WITH_JAVA -eq 1 ]]; then
  echo "== java sdk: build =="
  command -v mvn >/dev/null || { echo "maven not installed" >&2; exit 1; }
  mvn -q -f sdk/java-generated/pom.xml -DskipTests install
  jar=$(ls sdk/java-generated/target/parsedock-client-*.jar | grep -v -e sources -e javadoc -e tests | head -1)
  # Avoid a pipeline under `pipefail`: unzip gets SIGPIPE when grep exits early.
  listing=runtime/sdk-java-listing.txt
  unzip -Z1 "$jar" > "$listing"
  grep -q "org/openapitools/client/ApiClient.class" "$listing"
  echo "java client built: $jar"

  echo "== java sdk: integration =="
  # A live stack for the generated client. With --real (nightly) it runs the real
  # xberg engine on a scanned PDF, so the generated DocumentIR model is exercised
  # against real parser output, not only against the deterministic mock.
  PORT=18192
  INT_PORT=18193
  JAVA_ENGINES="mock"
  JAVA_FIXTURE="$ROOT/benchmark/fixtures/generated/pdf_text_en_3p.pdf"
  JAVA_EXTRA=()
  if [[ $WITH_REAL -eq 1 && -x "$ROOT/runtime/engines/xberg-cli/xberg" ]]; then
    JAVA_ENGINES="native,xberg"
    JAVA_FIXTURE="$ROOT/benchmark/fixtures/ocr/zh_dense.pdf"
    JAVA_EXTRA=(--xberg-bin "$ROOT/runtime/engines/xberg-cli/xberg"
                --xberg-lib-dir "$ROOT/runtime/engines/xberg-cli")
    echo "   engine: xberg (real), fixture: $(basename "$JAVA_FIXTURE")"
  else
    echo "   engine: mock"
  fi
  if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then
    echo "port $PORT busy; stop the previous run" >&2
    exit 1
  fi
  DATA="$(mktemp -d -t sdk-java-XXXXXX)"
  PARSEDOCK_MOCK_DELAY_MS=200 "$BIN" --role standalone \
    --listen "127.0.0.1:$PORT" --internal-listen "127.0.0.1:$INT_PORT" \
    --data-dir "$DATA" --work-dir "$DATA/work" --engines "$JAVA_ENGINES" \
    --internal-token itest --api-keys "java-it-token:java-it" "${JAVA_EXTRA[@]}" \
    --result-ttl 120s --cleanup-interval 1s >"$DATA/server.log" 2>&1 &
  SRV=$!
  cleanup() { kill "$SRV" 2>/dev/null || true; rm -rf "$DATA"; }
  trap cleanup EXIT
  for _ in $(seq 1 100); do
    curl -sf "http://127.0.0.1:$PORT/readyz" >/dev/null 2>&1 && break
    sleep 0.2
  done
  mvn -q -f sdk/java-client-test/pom.xml test \
    -Dparsedock.base="http://127.0.0.1:$PORT" \
    -Dparsedock.token=java-it-token \
    -Dparsedock.fixture="$JAVA_FIXTURE"
  cleanup
  trap - EXIT
else
  echo "== java sdk (skipped: --fast/--no-java) =="
fi

echo "sdk-check: pass"
