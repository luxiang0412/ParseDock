#!/usr/bin/env bash
# End-to-end container smoke test with real engines (PR-01).
#
#   scripts/container-smoke.sh
#
# Uses deploy/compose.cpu.yaml with throwaway credentials and ports, then:
#   1. waits for /readyz through an API replica,
#   2. parses a PDF in mode=fast        -> expects engine xberg,
#   3. parses a PDF in mode=accurate    -> expects engine docling-rs,
#   4. downloads on replica 1, ACKs on replica 2, expects 410 afterwards,
#   5. writes build/ci/container-smoke.json and tears the stack down.
#
# Requires: docker + compose, curl, jq, prebuilt images
# (PARSEDOCK_IMAGE / PARSEDOCK_WORKER_IMAGE, defaults parsedock:smoke and
# parsedock-worker-cpu:smoke).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${PARSEDOCK_IMAGE:-parsedock:smoke}"
WORKER_IMAGE="${PARSEDOCK_WORKER_IMAGE:-parsedock-worker-cpu:smoke}"
OUT_DIR="${OUT_DIR:-$ROOT/build/ci}"
API1="${SMOKE_API_PORT_1:-18080}"
API2="${SMOKE_API_PORT_2:-18082}"
PROJECT="${SMOKE_PROJECT:-parsedock-smoke}"
mkdir -p "$OUT_DIR"

TMP="$(mktemp -d)"
TOKEN="$(python3 -c 'import secrets;print(secrets.token_hex(24))')"
KEY="$(python3 -c 'import secrets;print(secrets.token_hex(24))')"
cat > "$TMP/env" <<ENV
PARSEDOCK_INTERNAL_TOKEN=$TOKEN
PARSEDOCK_API_KEYS=$KEY:smoke
API_PORT_1=$API1
API_PORT_2=$API2
PARSEDOCK_IMAGE=$IMAGE
PARSEDOCK_WORKER_IMAGE=$WORKER_IMAGE
ENV

COMPOSE=(docker compose -p "$PROJECT" -f "$ROOT/deploy/compose.cpu.yaml" --env-file "$TMP/env")

cleanup() {
  "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$TMP"
}
trap cleanup EXIT

fail() {
  echo "container smoke FAILED: $*" >&2
  "${COMPOSE[@]}" logs --tail=60 >&2 || true
  exit 1
}

wait_json() { # path, timeout seconds
  local path="$1"
  local timeout="${2:-180}"
  local deadline=$((SECONDS + timeout))
  while (( SECONDS < deadline )); do
    if curl -fsS -m 5 -H "Authorization: Bearer $KEY" "http://127.0.0.1:$API1$path" 2>/dev/null; then
      return 0
    fi
    sleep 2
  done
  return 1
}

echo "starting stack (image=$IMAGE worker=$WORKER_IMAGE)"
"${COMPOSE[@]}" up -d >/dev/null
wait_json "/readyz" 180 >/dev/null || fail "coordinator never became ready"
echo "ready"

submit() { # file, mode -> job id
  local file="$1" mode="$2"
  curl -fsS -X POST "http://127.0.0.1:$API1/v1/jobs?mode=$mode" \
    -H "Authorization: Bearer $KEY" -F "file=@$file" | jq -r .job_id
}

wait_done() { # job id, timeout -> final job json
  local jid="$1"
  local timeout="${2:-240}"
  local deadline=$((SECONDS + timeout))
  local status body
  while (( SECONDS < deadline )); do
    body="$(curl -fsS -H "Authorization: Bearer $KEY" "http://127.0.0.1:$API2/v1/jobs/$jid")" || return 1
    status="$(jq -r .status <<<"$body")"
    case "$status" in
      completed|failed|cancelled) printf '%s' "$body"; return 0 ;;
    esac
    sleep 1
  done
  return 1
}

FAST_ID="$(submit "$ROOT/benchmark/fixtures/generated/pdf_text_en_3p.pdf" fast)"
FAST_JOB="$(wait_done "$FAST_ID")" || fail "fast job did not finish"
[[ "$(jq -r .status <<<"$FAST_JOB")" == "completed" ]] || fail "fast job: $(jq -c '{status,error_code,error_message}' <<<"$FAST_JOB")"
[[ "$(jq -r .engine <<<"$FAST_JOB")" == "xberg" ]] || fail "fast route did not use xberg: $(jq -r .engine <<<"$FAST_JOB")"
echo "fast -> $(jq -r .engine <<<"$FAST_JOB")"

ACC_ID="$(submit "$ROOT/benchmark/fixtures/generated/pdf_text_zh_2p.pdf" accurate)"
ACC_JOB="$(wait_done "$ACC_ID")" || fail "accurate job did not finish"
[[ "$(jq -r .status <<<"$ACC_JOB")" == "completed" ]] || fail "accurate job: $(jq -c '{status,error_code,error_message}' <<<"$ACC_JOB")"
[[ "$(jq -r .engine <<<"$ACC_JOB")" == "docling-rs" ]] || fail "accurate route did not use docling-rs: $(jq -r .engine <<<"$ACC_JOB")"
echo "accurate -> $(jq -r .engine <<<"$ACC_JOB")"

DOWNLOAD_CODE="$(curl -sS -o "$TMP/result.json" -w '%{http_code}' \
  -H "Authorization: Bearer $KEY" "http://127.0.0.1:$API1/v1/jobs/$ACC_ID/result?format=json")"
[[ "$DOWNLOAD_CODE" == "200" ]] || fail "download on replica 1 returned $DOWNLOAD_CODE"
VERSION="$(curl -fsS -o /dev/null -D - -H "Authorization: Bearer $KEY" \
  "http://127.0.0.1:$API1/v1/jobs/$ACC_ID/result?format=json" \
  | tr -d '\r' | awk -F': ' 'tolower($1)=="x-result-version"{print $2}')"
[[ -n "$VERSION" ]] || fail "no X-Result-Version header"

ACK_CODE="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "{\"result_version\":\"$VERSION\"}" "http://127.0.0.1:$API2/v1/jobs/$ACC_ID/ack")"
[[ "$ACK_CODE" == "202" ]] || fail "ACK on replica 2 returned $ACK_CODE"
AFTER_CODE="$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $KEY" \
  "http://127.0.0.1:$API1/v1/jobs/$ACC_ID/result?format=json")"
[[ "$AFTER_CODE" == "410" ]] || fail "download after ACK returned $AFTER_CODE (want 410)"
echo "download=200 ack=202 after_ack=410"

jq -n --arg image "$IMAGE" --arg worker "$WORKER_IMAGE" \
      --arg fast_engine "$(jq -r .engine <<<"$FAST_JOB")" \
      --arg accurate_engine "$(jq -r .engine <<<"$ACC_JOB")" \
      --arg version "$VERSION" --arg download "$DOWNLOAD_CODE" \
      --arg ack "$ACK_CODE" --arg after "$AFTER_CODE" \
  '{status:"PASS", image:$image, worker_image:$worker,
    fast:{engine:$fast_engine}, accurate:{engine:$accurate_engine},
    result_version:$version, download_http:($download|tonumber),
    ack_http:($ack|tonumber), download_after_ack_http:($after|tonumber)}' \
  > "$OUT_DIR/container-smoke.json"
echo "wrote $OUT_DIR/container-smoke.json"
