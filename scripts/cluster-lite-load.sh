#!/usr/bin/env bash
# Cluster-lite load check (PR-07).
#
# Starts a single-host cluster-lite stack (coordinator + 2 API replicas + 2 workers)
# with the deterministic mock engine, then drives N jobs through one replica and
# reports latency/throughput. The mock delay is configurable, so the measured
# numbers isolate coordinator/scheduler overhead from parser cost.
#
# Usage: scripts/cluster-lite-load.sh [jobs] [concurrency] [mock_delay_ms]
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
JOBS="${1:-120}"
CONC="${2:-12}"
DELAY="${3:-200}"
BIN="${DOCPARSE_BIN:-bin/docparse}"
RUN="runtime/cluster-lite-load"
PORT_BASE="${PORT_BASE:-18200}"
COORD_PORT=$((PORT_BASE))
API1=$((PORT_BASE+1))
API2=$((PORT_BASE+2))
TOKEN="cluster-lite-key"

PIDS=()
rm -rf "$RUN"
mkdir -p "$RUN/data"
go build -o "$BIN" ./cmd/docparse

"$BIN" --role coordinator --internal-listen "127.0.0.1:$COORD_PORT" --internal-token tok \
  --data-dir "$RUN/data" --engines mock --rate-limit 5000 --rate-burst 2000 \
  --log-level info >"$RUN/coordinator.log" 2>&1 &
PIDS+=($!)
"$BIN" --role api --listen "127.0.0.1:$API1" --coordinator-url "http://127.0.0.1:$COORD_PORT" \
  --internal-token tok --dev-insecure --api-keys "$TOKEN:load-owner" --rate-limit 5000 --rate-burst 2000 \
  --max-upload-mb 32 >"$RUN/api1.log" 2>&1 &
PIDS+=($!)
"$BIN" --role api --listen "127.0.0.1:$API2" --coordinator-url "http://127.0.0.1:$COORD_PORT" \
  --internal-token tok --dev-insecure --api-keys "$TOKEN:load-owner" --rate-limit 5000 --rate-burst 2000 \
  --max-upload-mb 32 >"$RUN/api2.log" 2>&1 &
PIDS+=($!)
for i in 1 2; do
  PARSEDOCK_MOCK_DELAY_MS="$DELAY" "$BIN" --role worker --coordinator-url "http://127.0.0.1:$COORD_PORT" \
    --internal-token tok --engines mock --worker-id "w$i" --device-type cpu \
    >"$RUN/worker$i.log" 2>&1 &
  PIDS+=($!)
done

# Only the processes started here are terminated: a host may run other docparse
# instances for a different deployment or a parallel test.
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT

for port in "$COORD_PORT" "$API1" "$API2"; do
  for _ in $(seq 1 100); do
    if curl -sf "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 \
       || curl -sf -o /dev/null "http://127.0.0.1:$port/healthz" 2>/dev/null; then break; fi
    sleep 0.1
  done
done
sleep 1

printf 'cluster-lite load sample\n' > "$RUN/sample.txt"
echo "== $JOBS jobs, concurrency $CONC, mock delay ${DELAY}ms, 2 api replicas, 2 workers =="
python3 tests/load/loadgen.py --api "http://127.0.0.1:$API1" --token "$TOKEN" \
  --file "$RUN/sample.txt" --jobs "$JOBS" --concurrency "$CONC" --timeout 180 \
  --out "$RUN/load.json"
