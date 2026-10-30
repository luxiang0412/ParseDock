# CI (GitHub Actions) — PR-01 baseline

Everything CI runs is implemented in `scripts/ci-local.sh <phase>`, so a local
`make ci` executes exactly the same commands as the workflow. The workflow files
exist to schedule, parallelise and collect artifacts — not to hold logic.

## Workflows

| File | Trigger | Jobs | Blocking |
| --- | --- | --- | --- |
| `.github/workflows/ci.yml` | every PR, push to `main`, manual | `static`, `unit`, `contract`, `lifecycle`, `image`, `ci-summary` | yes (branch protection) |
| `.github/workflows/engine-changes.yml` | every PR (path-classified inside) | `detect`, `real-engines`, `container` | recommended for engine/docker changes |
| `.github/workflows/nightly.yml` | schedule 18:30 UTC, manual | `real-engines`, `container-smoke` (includes SBOM + licence classification) | no (evidence) |
| `.github/workflows/release-gate.yml` | tags `v*`, manual | `licence-gate` (blocking audit, no publish step) | yes, for releases |

`engine-changes.yml` runs on every PR but classifies changed paths first, so it
reports `success` in seconds when nothing engine-related changed. That keeps it
safe to include in branch protection.

## What each phase covers

| Phase | Commands (see the script for the exact list) | Why |
| --- | --- | --- |
| `static` | `verify_pins.py`, `gofmt -l`, `go vet`, `go build`, `actionlint` (pinned), `go mod tidy` drift | no unpinned inputs, no unformatted code, workflows valid |
| `unit` | `go test ./...`, `tests/unit/test_score.py` | behaviour + scoring rubric |
| `contract` | OpenAPI 3.1 validation (public + internal, pinned generator), `tests/contract/openapi_contract.py`, SDK regeneration drift | specs, router and generated SDKs cannot drift |
| `lifecycle` | `make acceptance` (T03–T16), `tests/integration/pr00_contract.py`, backup/restore | job lifecycle, ACK/TTL/lease/fencing, PR-00 regressions |
| `image` | control-plane image build, `--check`, image id/digest, `repro-check.sh`, licence audit (report) | reproducible artefact + clean control plane |
| `engines` | install xberg + docling-rs at locked versions, `acceptance-real`, quality bench subset, `score.py` | real parsers (nightly / engine changes) |
| `container` | build both images, `container-smoke.sh` (xberg + docling-rs through the API), SBOM + PASS/BLOCK classification | container parity with real engines |

## Reproducibility

* `images.lock.json` is the single source of truth: Go 1.24.4, base images
  (`golang@sha256:…`, `debian@sha256:…`, CUDA base), `openapi-generator@sha256:…`,
  Tika digest, xberg/docling-rs/model versions, actionlint version.
* `scripts/verify_pins.py` fails on drift: floating `FROM`, action tag instead of
  commit SHA, unpinned pip requirement, workflow tool image tag, `go.mod` bump.
* Docker build: pinned builder, `GOTOOLCHAIN=local`, `-trimpath -buildvcs=false
  -buildid=`. `scripts/repro-check.sh` proves two independent builds and the binary
  inside the image are byte-identical.
* SDK generation: pinned generator, `hideGenerationTimestamp=true`, target wiped
  first. CI fails when regeneration produces a diff.
* `.gitattributes` forces LF and marks binary fixtures so a Windows/macOS checkout
  with `core.autocrlf=true` cannot create false SDK drift.

## Artifacts

Each job uploads `build/ci/` (`ci-<phase>`):

* `<phase>.log` (full command output) and `<phase>.json` (per-step timings/status),
* `raw/` — only files produced by *this* run: acceptance JSON, PR-00 JSON, stack
  logs (lifecycle); `acceptance-real-*.json`, `benchmarks-*.csv`, `scores.json`
  (engines),
* `image-digest.txt`, `repro.txt`, `image-check.json`, `container-smoke.json`,
* `sbom/*.json` + `licence-classification.txt` (container phase).

## Licence policy in CI

* PR/control plane: audit runs in `report` mode and **fails only if the control
  plane carries GPL**. It is currently clean (0 GPL, 0 LGPL).
* Worker image: `AUDIT_MODE=report` records `worker=BLOCK` with
  `worker_gpl=['/opt/xberg/libx265.so.199']` in `licence-classification.txt`.
* `release-gate.yml` runs the audit in the default **enforce** mode on tags, so a
  release cannot be published while the worker image contains libx265
  (`docs/LICENSE-REVIEW.md`, item L1). There is no registry push step yet; when
  PR-08/PR-12 add one it must depend on this gate.

## Local equivalents

```bash
make ci              # static + unit + contract + lifecycle + image (≈2m40s on the dev host)
make ci-phase PHASE=contract
make verify-pins
make lint-workflows
make container-smoke # needs parsedock:smoke + parsedock-worker-cpu:smoke
make license-audit   # enforce mode (fails on GPL)
```

## Known limitations

* Runners are GitHub-hosted `ubuntu-24.04` (2 vCPU); the mock lifecycle phase is
  the critical path (~3m40s) and dominates the PR pipeline.
* No self-hosted GPU runner: GPU workers are verified manually (`docs/GPU-REPORT.md`),
  not in CI.
* Docker layer caching across runs is intentionally **not** used for the worker
  image: it is built `FROM` the locally built control-plane image, which a buildx
  container driver cannot resolve. The nightly container job therefore rebuilds
  both images (≈85 s on this project).
* Real-engine downloads (xberg tarball, docling-rs models ≈ 590 MB) are cached by
  `actions/cache` keyed on `images.lock.json`; a lock bump pays one cold download.
* The Python/PyYAML pin lives in `requirements-ci.txt`; the contract phase skips
  installation when PyYAML is already importable (PEP-668 managed dev machines).
