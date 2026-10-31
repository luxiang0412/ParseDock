# CI (GitHub Actions) — PR-01 baseline

Everything CI runs is implemented in `scripts/ci-local.sh <phase>`, so a local
`make ci` executes exactly the same commands as the workflow. The workflow files
exist to schedule, parallelise and collect artifacts — not to hold logic.

## Workflows

| File | Trigger | Jobs | Blocking |
| --- | --- | --- | --- |
| `.github/workflows/ci.yml` | every PR, push to `main`, manual | `static`, `unit`, `contract`, `lifecycle`, `image`, `ci-summary` | yes — see "Merge gate" |
| `.github/workflows/engine-changes.yml` | every PR (path-classified inside) | `detect`, `real-engines`, `container` | recommended for engine/docker changes |
| `.github/workflows/nightly.yml` | schedule 18:30 UTC, manual | `real-engines`, `sdks` (Python lifecycle + error matrix + **real engines** + Java build **and integration test** + version consistency), `container-smoke` (includes SBOM + licence classification) | no (evidence) |
| `.github/workflows/release-python.yml` | tag `v*`, manual | build sdist/wheel, `twine check`, install the wheel, publish to PyPI over OIDC | no (publishing) |
| `.github/workflows/release-maven.yml` | tag `v*`, manual | GPG preflight, build + sign, deploy a **pending** bundle to the Central Portal (`autoPublish=false`) | no (publishing) |
| `.github/workflows/release-gate.yml` | tags `v*`, manual | `licence-gate` (blocking audit, no publish step) | yes, for releases |

`engine-changes.yml` runs on every PR but classifies changed paths first, so it
reports `success` in seconds when nothing engine-related changed. That keeps it
safe to include in branch protection.

## What each phase covers

| Phase | Steps (the script is authoritative) | Why |
| --- | --- | --- |
| `static` | `verify_pins.py`, `gofmt -l`, `go vet`, `golangci-lint` (pinned, `.golangci.yml`), `govulncheck` (pinned, with the pinned toolchain), `ruff` (pinned, `ruff.toml`), `go build`, `actionlint` (pinned), `go mod tidy` drift | no unpinned inputs, no unformatted code, no lint findings, no reachable known vulnerability, workflows valid |
| `unit` | `go test ./cmd/... ./internal/... ./migrations/...` (never `./...`: it would pick up Go code under `web/node_modules`; `go.mod` also `ignore`s that directory), `test_score.py`, `test_ci_summary.py`, `test_fixtures.py` (corpus hashes), `test_gate.py` | behaviour, corpus and gate correctness |
| `sdk` | `scripts/sdk-check.sh --fast` — Python lifecycle + error matrix, one version across artefacts | the SDK contract: ACK rule, error codes/retry policy, SSE resume; catches an OpenAPI/IR change that would break a client
| `contract` | OpenAPI 3.1 validation (public + internal, pinned generator), `openapi_contract.py`, SDK regeneration drift (`sdk/python-generated`, `sdk/java-generated`), Document IR schema drift | specs, router, generated SDKs and the published schema cannot drift |
| `lifecycle` | `make acceptance` (T03–T17), `pr00_contract.py`, `backup_restore.py`, `pr02_contract.py` (keys/scopes/quota/audit), `pr07_tls.py` (mTLS), `pr09_metrics.py` (metrics + RC-5 + no content leak), `pr12_drill.py` (online backup + restore + RTO) | job lifecycle, ACK/TTL/lease/fencing/cancel, security edge, TLS, observability and backup regressions |
| `image` | control-plane image build, `--check`, image id/digest, `repro-check.sh`, licence audit (report + classification) | reproducible artefact + clean control plane |
| `engines` | install xberg + docling-rs at locked versions, `acceptance-real`, quality **and** perf bench on a fixed fixture scope, `summarize.py` + `score.py` into a run-scoped directory (`build/ci/bench-*`), `gate.py` | real parsers and the quality/latency gate (nightly / engine changes) |
| `container` | build both images, `container-smoke.sh` (xberg + docling-rs through the API), SBOM + PASS/BLOCK classification | container parity with real engines |

## Reproducibility

* `images.lock.json` is the single source of truth: Go 1.27.3, base images
  (`golang@sha256:…`, `debian@sha256:…`, CUDA base), `openapi-generator@sha256:…`,
  Tika digest, xberg/docling-rs/model versions, and the CI tools: actionlint,
  golangci-lint (release tarball, SHA-256 pinned in `scripts/install-golangci-lint.sh`),
  govulncheck (module version + `h1:` hash pinned in `scripts/install-govulncheck.sh`),
  ruff (`requirements-ci.txt`).
* `scripts/govulncheck.sh` runs the scan with `GOTOOLCHAIN=local` and refuses to start
  unless `go env GOVERSION` equals the locked Go version. `go run …/govulncheck@latest`
  under `GOTOOLCHAIN=auto` switches to a newer toolchain and reports on *its* standard
  library: on go1.24.4 that showed a clean result while the pinned toolchain had 42
  reachable stdlib vulnerabilities. Locally, put the pinned `go` first on `PATH`
  (`go install golang.org/dl/go1.27.3@latest && go1.27.3 download`, or the toolchain
  that `GOTOOLCHAIN=auto` already downloaded) before `ci-local.sh static`.
* `scripts/verify_pins.py` fails on drift: floating `FROM`, action tag instead of
  commit SHA, unpinned pip requirement, workflow tool image tag, `go.mod` bump.
* Docker build: pinned builder, `GOTOOLCHAIN=local`, `-trimpath -buildvcs=false
  -buildid=`. `scripts/repro-check.sh` proves two independent builds and the binary
  inside the image are byte-identical.
* SDK generation: pinned generator, `hideGenerationTimestamp=true`, target wiped
  first. CI fails when regeneration produces a diff.
* `.gitattributes` forces LF and marks binary fixtures so a Windows/macOS checkout
  with `core.autocrlf=true` cannot create false SDK drift.

## Merge gate

`ci-summary` aggregates the five job results through `scripts/ci_summary.py`
(unit-tested in `tests/unit/test_ci_summary.py`). A missing, cancelled, skipped or
failed job makes the summary fail, which fails the workflow run. The previous
inline `grep` implementation passed while `contract` was failing (substring match) —
that regression is covered by a test, and a negative-control PR proved the fix
(run `41041186706`: `contract` and `ci-summary` both red).

**Enforcement caveat:** this repository is private on a free GitHub plan, where
branch protection and rulesets return `HTTP 403`. The check names and the
enablement command are documented in `docs/history/PR01-REPORT.md` §6b; until the owner
upgrades the plan (or makes the repo public), a red pipeline is visible but not
technically unmergeable.

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
make ci              # static + unit + contract + lifecycle + image (≈3m30s on the dev host)
make ci-phase PHASE=contract
make verify-pins
make lint-workflows
make lint            # golangci-lint + ruff (pinned)
make vulncheck       # govulncheck with the pinned toolchain
make container-smoke # needs parsedock:smoke + parsedock-worker-cpu:smoke
make license-audit   # enforce mode (fails on GPL)
```

## Known limitations

* Runners are GitHub-hosted `ubuntu-24.04` (2 vCPU); the mock lifecycle phase is
  the critical path (~4m10s) and dominates the PR pipeline. It grew with PR-02/07/09/12
  runtime contracts; splitting it is a candidate if the wall time becomes painful.
* No self-hosted GPU runner: GPU workers are verified manually (`make gpu-check`,
  `docs/history/GPU-REPORT.md`), not in CI. `tests/integration/pr08_gpu.py` SKIPs cleanly without a
  GPU, so it is safe to run anywhere but proves nothing there.
* The Java half of `sdk-check` (Maven downloads, the integration test and the two
  examples) runs in the nightly `sdks` job; the PR `sdk` job only runs the Python
  layers and `--fast`,
  not on every PR; the Python half runs in `unit`.
* The engine quality gate is enforced in `engines` (nightly + engine-changes), not on every
  PR: installing xberg and docling-rs takes minutes.
* Docker layer caching across runs is intentionally **not** used for the worker
  image: it is built `FROM` the locally built control-plane image, which a buildx
  container driver cannot resolve. The nightly container job therefore rebuilds
  both images (≈85 s on this project).
* Real-engine downloads (xberg tarball, docling-rs models ≈ 590 MB) are cached by
  `actions/cache` keyed on `images.lock.json`; a lock bump pays one cold download.
* The Python/PyYAML pin lives in `requirements-ci.txt`; the contract phase skips
  installation when PyYAML is already importable (PEP-668 managed dev machines).
