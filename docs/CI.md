# CI (GitHub Actions) — PR-01 baseline

The Go/contract/lifecycle/image jobs are implemented in `scripts/ci-local.sh <phase>`,
so for those a local `make ci` (= `ci-local.sh all`: static, unit, contract, lifecycle,
image) executes the same commands as the workflow. Two blocking jobs are **not** phases
of `ci-local.sh all`:

* `web` holds its steps inline in `ci.yml` (`npm ci`, `api:types:check`, lint,
  typecheck, unit tests, build, dist secret scan); the local equivalent is
  `make web-test` (after `cd web && npm ci`).
* `sdk` runs `scripts/sdk-check.sh --fast`; the local equivalent is
  `bash scripts/sdk-check.sh --fast` (`make sdk-check` runs the full script, including
  the Java build).

A green `ci-local.sh all` therefore does not cover the whole PR pipeline; run the two
commands above as well before claiming CI parity.

## Workflows

| File | Trigger | Jobs | Blocking |
| --- | --- | --- | --- |
| `.github/workflows/ci.yml` | every PR, push to `main`, manual | `changes`, `static`, `unit`, `contract`, `lifecycle (1/4)`…`(4/4)`, `image`, `web`, `sdk`, `ci-summary`, `cache-prune` (main only) | yes — require `ci-summary` only, see "Merge gate" |
| `.github/workflows/engine-changes.yml` | every PR (path-classified inside) | `detect`, `real-engines`, `container` | recommended for engine/docker changes |
| `.github/workflows/nightly.yml` | schedule 18:30 UTC, manual | `real-engines`, `sdks` (check name `sdk-check`: Python lifecycle + error matrix + **real engines** + Java build **and integration test** + version consistency), `playground` (check name `playground-e2e`: Playwright against a real backend), `container` (check name `container-smoke`: includes SBOM + licence classification) | no (evidence) |
| `.github/workflows/release-python.yml` | tag `v*`, manual | build sdist/wheel, `twine check`, install the wheel, publish to PyPI over OIDC | no (publishing) |
| `.github/workflows/release-maven.yml` | tag `v*`, manual | GPG preflight, build + sign, deploy a **pending** bundle to the Central Portal (`autoPublish=false`) | no (publishing) |
| `.github/workflows/release-gate.yml` | tags `v*`, manual | `licence-gate` (blocking audit, no publish step) | yes, for releases |

`engine-changes.yml` runs on every PR but classifies changed paths first, so it
reports `success` in seconds when nothing engine-related changed. That keeps it
safe to include in branch protection.

## What each phase covers

| Phase | Steps (the script is authoritative) | Why |
| --- | --- | --- |
| `static` | `verify_pins.py`, `gofmt -l`, `go vet`, `golangci-lint` (pinned, `.golangci.yml`), `govulncheck` (pinned, with the pinned toolchain), `ruff` (pinned, `ruff.toml`), `actionlint` (pinned), `go mod tidy` drift, agent-skill sync | no unpinned inputs, no unformatted code, no lint findings, no reachable known vulnerability, workflows valid |
| `unit` | `go test ./cmd/... ./internal/... ./migrations/...` (never `./...`: it would pick up Go code under `web/node_modules`; `go.mod` also `ignore`s that directory), `test_score.py`, `test_ci_summary.py`, `test_ci_shards.py` (the lifecycle shards cover every T-case once), `test_fixtures.py` (corpus hashes), `test_gate.py` | behaviour, corpus and gate correctness |
| `sdk` | `scripts/sdk-check.sh --fast` — Python lifecycle + error matrix, one version across artefacts | the SDK contract: ACK rule, error codes/retry policy, SSE resume; catches an OpenAPI/IR change that would break a client |
| `web` (inline in `ci.yml`) | `npm ci`, `npm run api:types:check` (`schema.d.ts` vs `api/openapi.yaml`), lint, typecheck, `npm test`, `npm run build`, `check-dist-secrets.mjs` | the playground builds, its API types match the spec, no API key reaches `dist/` |
| `contract` | OpenAPI 3.1 validation (internal; the public spec is validated by the SDK regeneration),  `openapi_contract.py`, SDK regeneration drift (`sdk/python-generated`, `sdk/java-generated`), Document IR schema drift | specs, router, generated SDKs and the published schema cannot drift |
| `lifecycle` (CI: four shards `lifecycle-1`…`lifecycle-4`, see `LIFECYCLE_SHARD_CASES` in the script) | `make acceptance` (T03–T17 except T11, which needs real engines), `pr00_contract.py`, `backup_restore.py`, `pr02_contract.py` (keys/scopes/quota/audit), `pr07_tls.py` (mTLS), `pr09_metrics.py` (metrics + RC-5 + no content leak), `pr12_drill.py` (online backup + restore + RTO) | job lifecycle, ACK/TTL/lease/fencing/cancel, security edge, TLS, observability and backup regressions |
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

`ci-summary` aggregates the seven job results (`static`, `unit`, `contract`,
`lifecycle` — the aggregate of its four shards —, `image`, `web`, `sdk`) through
`scripts/ci_summary.py` (unit-tested in `tests/unit/test_ci_summary.py`). A missing,
cancelled, skipped or failed job makes the summary fail, which fails the workflow run.
`ci-summary` is the only check to require: it always runs (`if: always()`), and the
individual job names (the lifecycle shards, a skipped matrix job's unexpanded name) are
free to change.

**Documentation-only pull requests.** The `changes` job reads the pull request's file
list; when every file is Markdown, under `docs/` or `LICENSE*` — except
`docs/ERROR-CODES.md`, `docs/DOCUMENT-IR.md`, `.agents/**` and `sdk/**`, which checks
read — the build and test jobs are skipped and `ci-summary` is called with
`--docs-only`, which accepts `skipped` (never `failure` or `cancelled`). This is
deliberately not a workflow-level `paths-ignore`: a workflow that does not start never
reports, and a required check would stay pending forever. Pushes to `main` always run
everything. Probe: PR #64 (README-style change) finished in 15 s with `ci-summary`
green. The previous
inline `grep` implementation passed while `contract` was failing (substring match) —
that regression is covered by a test, and a negative-control PR proved the fix
(run `41041186706`: `contract` and `ci-summary` both red).

**Enforcement caveat:** no branch protection or ruleset is configured on `main` today
(when this was written the repository was private on a free plan, where both returned
`HTTP 403`). The check names and the
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
make ci              # static + unit + contract + lifecycle + image (191.7 s on the dev host, review batch)
make web-test        # the `web` job (after `cd web && npm ci`)
bash scripts/sdk-check.sh --fast   # the `sdk` job
make ci-phase PHASE=contract
make verify-pins
make lint-workflows
make lint            # golangci-lint + ruff (pinned)
make vulncheck       # govulncheck with the pinned toolchain
make container-smoke # needs parsedock:smoke + parsedock-worker-cpu:smoke
make license-audit   # enforce mode (fails on GPL)
```

## Speed

PR wall time went from 226 s (median of 50 runs, Oct 2026) to under 90 s with warm caches.
What makes it fast, and what to keep when editing the workflows:

* **Concurrency.** A pull request cancels its own superseded runs. A push to `main` is
  grouped by its commit SHA, so every main commit runs to completion (a ref-wide group
  with `cancel-in-progress` used to cancel main runs).
* **Caches, written on main only.** `.github/actions/go` restores the pinned Go toolchain
  (the runner image does not ship it), `~/.cache/go-build` and `~/go/pkg/mod` as one entry
  per kind, then runs setup-go (which finds the toolchain locally). Three kinds, each
  saved by exactly one job on pushes to `main`: `static` (also golangci-lint's cache and
  the pinned tool binaries in `runtime/bin`, so govulncheck is not compiled per run),
  `unit` (also read by `contract`), `bin` (saved by `sdk`, read by lifecycle, image,
  nightly and engine-changes). Pull requests only restore — a PR-scoped entry is
  readable by no other PR — and `cache-prune` deletes superseded entries after main
  saves new ones. setup-go's own cache is off: it is saved by whichever job finishes
  first, which was `contract`, so every job used to restore 26 MB and compile from scratch.
* **The 729 MB engine cache** is saved by nightly on `main` and only restored by
  engine-changes; saving it per pull request had filled the 10 GB repository cache.
* **lifecycle is sharded.** Shard 1 (T09 + T17) is the floor: T09 waits ~51 s for a worker
  lease to expire. `tests/unit/test_ci_shards.py` fails if a T-case is in no shard.
* **No duplicate work.** `unit` does not run sdk-check (that is the `sdk` job); `static`
  does not build the binary; `contract` does not validate the public spec twice.
* **Image layers are not cached** (no buildx/GHA cache): the image job is not on the
  critical path, its Go build changes with every Go PR, and setup-buildx plus `--load`
  would cost about what the apt layer saves.
* The `changes` gate costs ~7 s per run (one extra job start); it buys a 15 s run for a
  documentation-only pull request instead of a full one.

## Known limitations

* Runners are GitHub-hosted `ubuntu-24.04` (2 vCPU). The lifecycle shards are the critical
  path (~70 s per job, ~56 s of it tests); shortening T09 would mean a shorter worker lease
  in the test stack, which is a test-design decision, not a CI one.
* A pull request opened right after a `go.mod`/`go.sum` change on `main`, or before the
  first main run after this layout landed, finds no cache and compiles cold (~140 s wall).
* No self-hosted GPU runner: GPU workers are verified manually (`make gpu-check`,
  `docs/history/GPU-REPORT.md`), not in CI. `tests/integration/pr08_gpu.py` SKIPs cleanly without a
  GPU, so it is safe to run anywhere but proves nothing there.
* The Java half of `sdk-check` (Maven downloads, the integration test and the two
  examples) runs in the nightly `sdks` job, not on every PR; the PR `sdk` job runs
  `sdk-check.sh --fast` (Python layers + version consistency).
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
