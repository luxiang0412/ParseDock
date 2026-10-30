#!/usr/bin/env bash
# CI job implementations, shared by GitHub Actions and by local runs (PR-01).
#
#   scripts/ci-local.sh <phase>
#
# Phases (one per CI job, so the workflow and a local run execute the *same*
# commands):
#   static      pinned-input verification, gofmt, go vet, build
#   unit        go test ./... + scoring unit tests
#   contract    OpenAPI validation, spec/SDK/router drift, SDK regeneration drift
#   lifecycle   mock acceptance (T03-T16), PR-00 runtime contract, backup/restore
#   image       control-plane image build, smoke, digest, licence audit (report)
#   engines     install real engines, real-engine acceptance, bench + score
#   container   worker image build, container smoke with real engines, SBOM
#   all         static + unit + contract + lifecycle + image (local full run)
#
# Every phase writes build/ci/<phase>.log and build/ci/<phase>.json (timings,
# status). Artifacts are copied to build/ci/ for upload.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/build/ci"
mkdir -p "$OUT"
PHASE="${1:?usage: ci-local.sh <phase|all>}"
LOG="$OUT/$PHASE.log"
: > "$LOG"

STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
STEPS_JSON="$OUT/.$PHASE.steps.jsonl"
: > "$STEPS_JSON"
FAILED_STEP=""

step() { # name, command...
  local name="$1"; shift
  local t0 t1 status
  echo "==> $name" | tee -a "$LOG"
  t0=$(date +%s%3N)
  if "$@" 2>&1 | tee -a "$LOG"; then
    status="pass"
  else
    status="fail"
  fi
  t1=$(date +%s%3N)
  python3 - "$STEPS_JSON" "$name" "$status" "$((t1 - t0))" <<'PY'
import json, sys
path, name, status, ms = sys.argv[1:5]
with open(path, "a") as f:
    f.write(json.dumps({"step": name, "status": status, "duration_ms": int(ms)}) + "\n")
PY
  if [[ "$status" == "fail" ]]; then
    FAILED_STEP="$name"
    return 1
  fi
  return 0
}

phase_static() {
  step "verify pins" python3 "$ROOT/scripts/verify_pins.py" || return 1
  step "gofmt" make -C "$ROOT" fmt-check || return 1
  step "go vet" bash -c "cd '$ROOT' && go vet ./..." || return 1
  step "go build" make -C "$ROOT" build || return 1
  # Workflow linting uses the pinned actionlint and no external shellcheck so the
  # result does not depend on the runner image.
  step "workflow lint (actionlint)" bash -c "bash '$ROOT/scripts/install-actionlint.sh' >/dev/null && '$ROOT/runtime/bin/actionlint' -no-color -shellcheck= '$ROOT/.github/workflows/'*.yml" || return 1
  step "go mod tidy drift" bash -c "cd '$ROOT' && cp go.mod /tmp/go.mod.ci && cp go.sum /tmp/go.sum.ci && go mod tidy && (git diff --exit-code -- go.mod go.sum || (cp /tmp/go.mod.ci go.mod && cp /tmp/go.sum.ci go.sum && false))" || return 1
}

phase_unit() {
  step "go test" bash -c "cd '$ROOT' && go test ./... -count=1 -timeout 300s" || return 1
  step "scoring unit tests" python3 "$ROOT/tests/unit/test_score.py" || return 1
}

phase_contract() {
  # CI runners start without PyYAML; local/dev machines may already have it (and
  # some are PEP-668 managed), so only install when the import fails.
  step "install CI python deps" bash -c "python3 -c 'import yaml' 2>/dev/null || python3 -m pip install --disable-pip-version-check -q -r '$ROOT/requirements-ci.txt'" || return 1
  step "openapi validate (public)" docker run --rm -v "$ROOT:/local" "$OPENAPI_GENERATOR_IMAGE" \
    validate -i /local/api/openapi.yaml || return 1
  step "openapi validate (internal)" docker run --rm -v "$ROOT:/local" "$OPENAPI_GENERATOR_IMAGE" \
    validate -i /local/api/openapi.internal.yaml || return 1
  step "contract (specs, SDK, router drift)" python3 "$ROOT/tests/contract/openapi_contract.py" || return 1
  step "sdk regeneration drift" bash -c "cd '$ROOT' && make sdk >/dev/null && git diff --exit-code -- sdk" || return 1
}

phase_lifecycle() {
  step "build binary" make -C "$ROOT" build || return 1
  step "acceptance lifecycle (T03-T16)" python3 "$ROOT/tests/integration/acceptance.py" --profile lifecycle || return 1
  step "PR-00 runtime contract" python3 "$ROOT/tests/integration/pr00_contract.py" || return 1
  step "backup/restore" python3 "$ROOT/tests/integration/backup_restore.py" || return 1
}

phase_image() {
  step "build control-plane image" bash -c "docker build -f '$ROOT/docker/Dockerfile' -t parsedock:ci '$ROOT'" || return 1
  step "image self-check" bash -c "docker run --rm parsedock:ci --check > '$OUT/image-check.json'" || return 1
  step "image digest" bash -c "docker image inspect parsedock:ci --format 'id={{.Id}}' > '$OUT/image-digest.txt'; docker image inspect parsedock:ci --format '{{if .RepoDigests}}{{index .RepoDigests 0}}{{end}}' | sed 's/^/repo_digest=/' >> '$OUT/image-digest.txt'" || return 1
  step "reproducible build" bash "$ROOT/scripts/repro-check.sh" parsedock:ci || return 1
  step "licence audit (report mode)" bash -c "AUDIT_MODE=report SBOM_DIR='$OUT/sbom' bash '$ROOT/scripts/license_audit.sh' parsedock:ci" || return 1
  step "licence audit classification" bash -c "python3 - '$OUT/sbom/parsedock_ci.json'" <<'PY' || return 1
import json, sys
doc = json.load(open(sys.argv[1]))
if doc.get("gpl_libraries"):
    print(f"unexpected GPL finding in the control-plane image: {doc['gpl_libraries']}")
    sys.exit(1)
print(f"control plane clean: {doc['packages_count']} packages, {len(doc['lgpl_libraries'])} LGPL")
PY
}

phase_engines() {
  step "install real engines" bash "$ROOT/scripts/ci-install-engines.sh" xberg,docling-rs || return 1
  step "build binary" make -C "$ROOT" build || return 1
  step "acceptance real engines (T01/T02/T11)" python3 "$ROOT/tests/integration/acceptance.py" \
    --profile real --engines native,xberg,docling-rs || return 1
  step "bench quality (4 fixtures)" bash -c "cd '$ROOT' && python3 benchmark/scripts/bench.py quality \
    --engines native,xberg,docling-rs --only pdf_native_en,pdf_scanned_en,docx,csv" || return 1
  step "score" bash -c "cd '$ROOT' && python3 benchmark/scripts/summarize.py && python3 benchmark/scripts/score.py --write" || return 1
  step "scoring unit tests" python3 "$ROOT/tests/unit/test_score.py" || return 1
}

phase_container() {
  # CI builds the images with buildx/GHA layer caching; SKIP_IMAGE_BUILD keeps this
  # phase reusable there while a local run builds them plainly.
  if [[ "${SKIP_IMAGE_BUILD:-0}" != "1" ]]; then
    step "build control-plane + worker images" bash "$ROOT/scripts/build-images.sh" smoke || return 1
  else
    step "images prebuilt (SKIP_IMAGE_BUILD=1)" bash -c "docker image inspect parsedock:smoke parsedock-worker-cpu:smoke >/dev/null" || return 1
  fi
  step "container smoke with real engines" bash "$ROOT/scripts/container-smoke.sh" || return 1
  step "licence audit (report mode, both images)" bash -c "AUDIT_MODE=report SBOM_DIR='$OUT/sbom' bash '$ROOT/scripts/license_audit.sh' parsedock:smoke parsedock-worker-cpu:smoke" || return 1
  step "licence classification" python3 - "$OUT/sbom/parsedock_smoke.json" "$OUT/sbom/parsedock-worker-cpu_smoke.json" <<'PY' || return 1
import json, sys
control = json.load(open(sys.argv[1]))
worker = json.load(open(sys.argv[2]))
if control.get("gpl_libraries"):
    print("control plane must stay GPL-free"); sys.exit(1)
if not any("libx265" in p for p in worker.get("gpl_libraries", [])):
    print("NOTE: worker image no longer bundles libx265 — revisit licence item L1 "
          "and remove the documented BLOCK"); sys.exit(0)
print("worker image BLOCKED as expected: libx265 (GPL-2.0) — external distribution "
      "remains blocked until L1 is signed off")
PY
}

run_phase() {
  case "$1" in
    static) phase_static ;;
    unit) phase_unit ;;
    contract) phase_contract ;;
    lifecycle) phase_lifecycle ;;
    image) phase_image ;;
    engines) phase_engines ;;
    container) phase_container ;;
    all) phase_static && phase_unit && phase_contract && phase_lifecycle && phase_image ;;
    *) echo "unknown phase $1" >&2; return 2 ;;
  esac
}

export OPENAPI_GENERATOR_IMAGE="${OPENAPI_GENERATOR_IMAGE:-$(python3 - "$ROOT/images.lock.json" <<'PY'
import json, sys
print(json.load(open(sys.argv[1]))["images"]["openapi_generator"])
PY
)}"

if run_phase "$PHASE"; then
  STATUS="pass"
else
  STATUS="fail"
fi

FINISHED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
python3 - "$OUT/$PHASE.json" "$PHASE" "$STATUS" "$STARTED_AT" "$FINISHED_AT" "$STEPS_JSON" "$FAILED_STEP" <<'PY'
import json, sys
path, phase, status, started, finished, steps_path, failed = sys.argv[1:8]
steps = [json.loads(line) for line in open(steps_path) if line.strip()]
json.dump({"phase": phase, "status": status, "started_at": started, "finished_at": finished,
           "steps": steps, "failed_step": failed or None,
           "total_ms": sum(s["duration_ms"] for s in steps)},
          open(path, "w"), indent=2)
print(f"phase {phase}: {status} in {sum(s['duration_ms'] for s in steps)/1000:.1f}s"
      + (f" (failed: {failed})" if failed else ""))
PY

# Collect the evidence CI uploads as artifacts.
if [[ "$PHASE" == "lifecycle" || "$PHASE" == "all" ]]; then
  mkdir -p "$OUT/raw"
  cp -a "$ROOT"/benchmark/results/raw/acceptance-*.json "$OUT/raw/" 2>/dev/null || true
  cp -a "$ROOT"/benchmark/results/raw/pr00-*.json "$OUT/raw/" 2>/dev/null || true
  cp -a "$ROOT"/runtime/logs/acceptance/*.log "$OUT/raw/" 2>/dev/null || true
fi
if [[ "$PHASE" == "engines" || "$PHASE" == "all" ]]; then
  mkdir -p "$OUT/raw"
  cp -a "$ROOT"/benchmark/results/summary/*.csv "$OUT/raw/" 2>/dev/null || true
  cp -a "$ROOT"/benchmark/results/summary/scores.json "$OUT/raw/" 2>/dev/null || true
  cp -a "$ROOT"/benchmark/results/raw/acceptance-real-*.json "$OUT/raw/" 2>/dev/null || true
fi

[[ "$STATUS" == "pass" ]]
