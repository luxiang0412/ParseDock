# AGENTS.md — working notes for AI agents in this repo

ParseDock is a document-parsing service: one Go binary with four roles
(`standalone | api | coordinator | worker`), SQLite (WAL) + the local filesystem as the
only state, and pluggable parser engines (xberg, docling-rs, docling-serve, mineru, tika,
native, mock). Read `README.md` for the product view and `docs/ARCHITECTURE.md` for the
topology; this file is the operational contract for making changes here.

## Hard rules

1. **`test_data/` is sensitive real corporate audit material.** Never commit it, never
   upload it, never put document text, file names or paths from it into logs, docs, test
   output or commit messages. Any harness that touches it must hash file identities by
   default (see `tests/integration/batch_parse.py`).
2. **No infrastructure drift.** No Redis, Postgres, S3, message queue, Kubernetes
   operator or cloud SDK. State is SQLite + local disk; scaling means more workers and
   stateless API replicas.
3. **Do not claim success you did not observe.** "It should work" is not a result. Run the
   command, or write down that it was not verified and why.
4. **Only change the slice you were asked for.** No drive-by refactors, renames or
   dependency bumps. New dependencies need a licence check (`docs/LICENSE-REVIEW.md`) and
   a justification in the PR.
5. Preserve the public contracts unless the task is to change them: `api/openapi.yaml`
   (public), `api/openapi.internal.yaml`, Document IR `doc-ir/0.1`, error codes, ACK/TTL
   semantics, the worker protocol.

## Architecture at a glance

```
client → api replica (auth, scopes, rate limit, quota pre-check) → coordinator (state)
                                                                      ↑ claim/heartbeat
                                                                   worker (parsers)
```

* `api` is stateless: it reverse-proxies to the coordinator and injects the owner identity.
* `coordinator` owns SQLite, the file tree, routing, leases, retention and metrics.
* `worker` claims one job at a time per slot, downloads the input, runs an engine, posts
  the result; it never talks to the database directly.
* Two independent lifecycle dimensions live in `internal/jobs/job.go`:
  `ProcessingStatus` (queued → running → completed/failed/cancelled) and
  `RetentionStatus` (active → release_pending/expiry_pending → deleting → released/expired).
  **ACK is a deletion request**, not an acknowledgement of receipt.

## Layout

| Path | What lives there |
| --- | --- |
| `cmd/docparse/main.go` | flags, role wiring, `--check` and `--version` |
| `internal/api` | public gateway, hashed keys/scopes, rate limiter, throttle forwarding |
| `internal/coordinator` | service (routing/claim/complete/fail), HTTP, cleanup, quota, metrics |
| `internal/worker` | claim/execute/heartbeat, scratch sweeping, escalation, VRAM reporting |
| `internal/engines` | engine adapters, capabilities, `exec.go` (RSS ceiling), `ooxml.go` (XLSX formulas) |
| `internal/storage` | file layout, sniffing/validation, manifests, limits |
| `internal/documentir` | IR types + embedded JSON Schema (mirrored to `api/`) |
| `internal/jobs` | status/retention constants, stable error codes, ID/token generation |
| `migrations/*.sql` | applied at start via `migrations/embed.go` — append, never edit old files |
| `api/openapi*.yaml` | public + internal specs (the contract) |
| `tests/` | `contract/`, `integration/` (real processes), `unit/`, `load/`, `chaos/` |
| `scripts/ci-local.sh` | the single source of truth for CI phases |
| `benchmark/` | fixtures (`generated/`, 110-doc `corpus/`), bench/score/gate scripts |
| `web/` | React + Vite playground (its own `package.json`; not part of the Go build) |
| `docs/` | design + operations; `docs/PHASE1-SUMMARY.md` is the status page, `docs/CHANGELOG.md` what shipped, `docs/RELEASING.md` how a release is cut |
| `sdk/` | hand-written Python package (`sdk/python`), generated clients (`sdk/*-generated`, never hand-edit) and the Java integration test (`sdk/java-client-test`) |
| `examples/` | runnable Python and Java projects (`make run`) |
| `.agents/skills/` | agent-facing guides for the two SDKs |

## Commands

```bash
make build          # → runtime/bin/docparse (bin/ is a gitignored stray; use runtime/bin)
make test           # go vet + go test ./... + scoring unit tests (the rest run in `ci-local.sh unit`)
bash scripts/ci-local.sh all      # exactly what CI runs: static, unit, contract, lifecycle, image (~3.5 min)
bash scripts/ci-local.sh lifecycle   # T03–T17 + PR-00/02/07/09/12 runtime contracts (fast feedback)
bash scripts/ci-local.sh engines     # real engines + bench + quality gate (needs installed engines)
make sdk            # regenerate both SDKs (dockerised, pinned); never hand-edit sdk/*-generated
make sdk-check      # Python e2e + CLI + one version across artefacts + Java build
make acceptance     # T03–T16 (mock engine)
make drill          # online backup + restore, prints RTO
make gate           # benchmark quality/latency gate vs benchmark/results/baseline.json
cd web && npm ci    # playground deps (lockfile-exact); then npm run lint/typecheck/test/build
make web-test       # lint + typecheck + unit tests + build + dist secret scan
make web-e2e        # Playwright against a real backend (E2E_ENGINES=native,xberg for a PDF)
make playground-up  # CPU stack + playground via docker compose (needs deploy/.env)
```

Always `pkill -x docparse` before starting a stack in a script; leftover processes cause
port clashes and confusing failures in the integration tests.

## Change workflow

1. Branch: `pr-XX-<slug>`. Commit style is conventional commits
   (`fix(coordinator): …`, `feat(gpu): …`) with the *why* in the body, not just the what.
2. Implement, then verify:
   * Go changes → `make test` (and `go test -race ./internal/...` when touching
     concurrency: quota admission, leases, metrics, cleanup).
   * API/spec changes → `make sdk` must produce **no diff** outside `sdk/*-generated`
     except the regenerated files you intend to commit; `tests/contract/openapi_contract.py`
     fails on router↔spec drift (new routes must be added to the spec).
   * Runtime behaviour → `bash scripts/ci-local.sh lifecycle`, then `all`.
3. Update the documents that would otherwise become lies: `docs/IMPLEMENTATION-PLAN.md`
   (per-PR status), `ARCHITECTURE.md`, `DEPLOYMENT.md`, `JOB-LIFECYCLE.md`, `SLO.md`,
   `RUNBOOK.md`, `api/openapi*.yaml`. A behaviour change without a doc change is an
   incomplete change.
4. Open a PR with the evidence (command output, numbers with units, what was *not*
   verified). Merge only after CI is green.

## Traps that have already bitten us

* **`make sdk` regenerates whole directories.** Hand-editing `sdk/python-generated` or
  `sdk/java-generated` is silently thrown away; the drift check compares only those two
  directories, so the hand-written wrapper in `sdk/python/parsedock_sdk/` is safe to edit.
* **Migrations:** append a new file. Editing `0001_init.sql` desynchronises existing
  databases (and `retention_target`, `free_vram_bytes`, `owner_storage` were added later).
* **SQLite WAL:** the DSN sets `_txlock=immediate`. A deferred transaction that reads
  then writes can fail with `SQLITE_BUSY_SNAPSHOT`, and `busy_timeout` does not help.
* **Two-phase state:** never move `RetentionStatus` without the matching `ProcessingStatus`
  (and vice versa). `deleteJobUnlessLeased` exists because deletion must be arbitrated by
  `active → release_pending` **before** files are removed; download leases protect both
  clients and online backups.
* **Quota is enforced twice** (admission *and* completion). A result can be much larger
  than its input, and pending jobs all observe the same used-bytes count.
* **GPU workers run one task at a time** (`max_concurrency` is capped at 1 on registration):
  per-card VRAM cannot be partitioned. VRAM is reported per *device*, not summed across cards,
  and only for the card the parser will use (`CUDA_VISIBLE_DEVICES[0]`).
* **GPU worker scratch must not be the volume root**: the scrubber deletes directories older
  than an hour, which would eat the multi-GB model cache. Use `/work/scratch`, keep
  `HOME`/`HF_HOME`/`XDG_CACHE_HOME` in the volume.
* **`--tls-client-ca` without `--tls-cert/--tls-key` is a startup error** on purpose;
  do not relax it into "plaintext with a warning".
* **Document IR schema is drift-checked.** Edit
  `internal/documentir/schema/document-ir-0.1.schema.json`, then `make schema` to publish it.
* **Error codes are a public contract.** Add to `internal/jobs/job.go` + the OpenAPI enum +
  `docs/DEPLOYMENT.md`; classifiers live in `internal/engines/exec.go` (`ClassifyCLIError`).
* **The Go binary and both SDKs must share one version** (`scripts/check_versions.py`).
* **The playground never holds the API key** — the dev/preview proxy and nginx inject it from the
  environment. Never rename it to `VITE_*` (Vite inlines those) and never ACK a result
  automatically; `web/scripts/check-dist-secrets.mjs` fails CI if a key reaches `dist/`.
* **`web/src/api/schema.d.ts` is generated** from `api/openapi.yaml` (`npm run api:types`); the
  CI drift check fails if it is stale, so an API change and the client move together.
* **Benchmark inputs must be run-scoped.** `PARSEDOCK_BENCH_RAW` / `PARSEDOCK_BENCH_SUMMARY`
  point at `build/ci/bench-*` in CI; without that, `summarize.py` happily mixes in older runs.

## Deliberate gaps (do not "fix" without a decision)

* Worker image **external distribution** is blocked by L1: `libx265` (GPL-2.0) inside the
  xberg tarball. The control-plane image is clean.
* No automatic coordinator failover/leader election; one stateful coordinator, restart is the
  documented recovery (`docs/RUNBOOK.md`).
* No OpenTelemetry SDK. Correlation is `X-Request-Id` + audit lines + `job_events`; see
  `docs/SLO.md` §3 before adding an exporter.
* `image_ref` in the IR is reserved; artifacts are not downloadable yet.
* 550–770 MiB expanded XLSX need sheet-window extraction; they currently fail with
  `expanded_too_large` by design.
