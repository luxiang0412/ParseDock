# Phase 1 summary

> One page: what shipped, what was verified, and what is still open. Details live in the
> per-PR sections of `docs/IMPLEMENTATION-PLAN.md` and the referenced reports.

## Shipped

| PR | Slice | Status | Evidence |
| --- | --- | --- | --- |
| 01 | CI baseline, reproducible builds | **DONE** | 6 workflows (`ci`, `engine-changes`, `nightly`, `release-gate`, `release-python`, `release-maven`), `scripts/ci-local.sh`, pinned inputs, byte-identical rebuilds; PR runs 3 m 53 s – 4 m 20 s |
| 02 | Public-edge security | **DONE** | hashed keys + scopes, per-owner quota, audit lines, request-id; `pr02_contract.py` 6/6 |
| 03 | Observable cancellation | **DONE** | `cancel_requested_at`, worker discards late results, per-engine ceilings; T17 |
| 04 | Engines | **DONE** | artefact SHA-256 pins, legacy OLE2, scan→accurate escalation, RSS ceiling/OOM code |
| 05 | Document IR v0.1 | **DONE** | published JSON Schema + drift check, golden Docling→IR, XLSX formula enrichment |
| 06 | Storage & quota | **DONE** | transactional `owner_storage`, completion-time quota, atomic retention, gzip downloads |
| 07 | Cluster-lite | **PARTIAL** | mTLS end-to-end (`pr07_tls.py` 8/8), load 8.8 jobs/s at 88 % of the parser ceiling; **two-host + real partition not measured** |
| 08 | GPU worker | **PARTIAL** | image built and verified (`pr08_gpu.py` 4/4: MinerU on GPU, 18.8 GiB free VRAM reported, `gpu_required` completed), VRAM admission at routing **and** claim; **docling-serve CUDA unwired, external distribution blocked by L1** |
| 09 | Observability | **DONE** (no OTel) | dependency-free Prometheus surface, RC-5 throttling aggregation, `SLO.md`, dashboard + 7 alerts |
| 10 | SDKs | **PARTIAL** | Python package **live on PyPI** (`pip install parsedock`); Java client `com.luxiangs:parsedock-client` **live on Maven Central** (0.1.0, 2026-10-31); one version across artefacts; lifecycle + error-matrix + real-engine + Java integration tests; **`schemathesis` fuzzing and the async client not done** |
| 11 | Corpus & quality gate | **PARTIAL** | 110-document reproducible corpus, hash-checked, run-scoped gate (>2 % quality / >15 % latency); **no TEDS/APTED, no public licensed corpus** |
| 12 | Runbooks | **DONE** | online backup (lease-aware, verified), measured restore drill (RTO 0.1 s, 7/7), `RUNBOOK.md` |

## Verified end to end (real processes, real files, real engines where applicable)

* **Real corpus** (re-run on current `main`, `.doc`/`.xls` now actually sent): 972 eligible documents,
  **943 completed (97.0 %)** (938 / 96.5 % before the two follow-up fixes), 23.34 M chars, 6 612 tables; 3.6 files/s at client c=16 with the four
  parser slots 90 % busy (`docs/REAL-CORPUS-TEST.md` §2a). Three defects found in the first
  pass (PDF trailer, `/Encrypt`, `expanded_too_large`) plus two in the re-run (relative
  `--work-dir` broke every CLI parse; the claim poll interval was a hard-coded 1 s).
* **CI**: eight blocking PR checks in `ci.yml` (`static`, `unit`, `contract`, `lifecycle`,
  `image`, `web`, `sdk` and the `ci-summary` gate); a negative-control PR proved the merge
  gate fails on a contract regression.
* **GPU**: `parsedock-worker-gpu:0.1.0` runs MinerU on the RTX 2080 Ti and completes a
  `gpu_required` scanned PDF; three image defects fixed (venv shebang, volume ownership,
  `HOME=/root`).
* **Security**: no client certificate → handshake rejected; foreign CA → rejected; plaintext
  on the TLS port → rejected; api replica without a client certificate → 502.
* **Backup**: online snapshot verified (integrity + per-artifact SHA-256 + manifests),
  active result restorable with the same `result_version`, released result stays released
  with `410`, RTO measured.

## Open, and why

| Item | Blocker |
| --- | --- |
| Worker image **external** distribution | **L1 licence**: `libx265` (GPL-2.0) inside the xberg tarball. Needs a legal decision (rebuild without HEVC, GPL source offer, or replace the engine). The control-plane image is clean and publishable. |
| Two-host cluster measurement, 30 s network partition | needs a second host; procedure documented in `CLUSTER-LITE.md` §8 |
| Branch protection / required checks | private repo on the free plan → `HTTP 403: Upgrade to GitHub Pro or make this repository public`; checks are red on failure but not technically unmergeable |
| docling-serve on CUDA | upstream device option undocumented in the tested build |
| 14 oversized XLSX (550–770 MiB expanded) | needs sheet-window extraction (PR-06 follow-up, RC-2) |
| TEDS/APTED, public licensed corpus, `schemathesis` | scoped out of PR-10/11 with reasons recorded there |
| Legacy `.doc`/`.xls` from the real corpus | supported by engines and allow-list now; not yet re-run against the corporate corpus after PR-04 |

## How to reproduce the claims

```bash
make ci                # static, unit, contract, lifecycle, image (not the web/sdk jobs)
make web-test          # the CI `web` job
bash scripts/sdk-check.sh --fast   # the CI `sdk` job
make acceptance        # T03–T17 except T11 (mock engine)
make drill             # backup + restore with RTO
make gpu-check         # GPU image end to end (needs a GPU host)
make gate              # engine quality/latency gate
make sdk-check         # Python + CLI + Java SDK + version consistency
make load              # cluster-lite throughput
```

Sensitive corporate material (`test_data/`) is local-only and never committed; the batch
harness redacts file identities by default.

## Playground (beyond Phase 1)

`web/` adds the ParseDock Playground: upload → parse → side-by-side original vs Markdown, with
SSE progress, cancel, explicit release (never automatic), local-file previews (PDF via pdf.js,
images, text/CSV, DOCX, XLSX) and a same-origin proxy that keeps the API key out of the browser.
Docker profile `playground`, unit + Playwright E2E, screenshots in `docs/screenshots/`. Details:
`docs/PLAYGROUND-IMPLEMENTATION.md`, evidence: `docs/history/PLAYGROUND-TEST-REPORT.md`.

## Post-Phase-1 audits

Three whole-repository audits (static, by a reviewing model, then verified against the code)
found 33 issues; all were fixed except two documented non-code items. The P0 and the
high-severity P1s were real: cleanup deleting live files after a failed lookup, an ACK that
authorised deleting an in-flight job's input, two completions destroying each other's results,
and a render path that could allocate ~20 billion strings from a 500 KB CSV. See
`docs/history/AUDITS.md` for the list, the reasoning, and what was deliberately left alone
(page-parallel OCR, legacy result migration, the L1 licence).

## Review hardening batch (2026-11-01, unreleased)

Four review-fix branches (worker/engines, coordinator, API security, toolchain/contract) were
merged on `review/integration`: separate worker token, redacted `/readyz`, failed-auth
limiter, 16-character key floor, stricter request validation, heartbeat tolerance, OOM
classification, Go 1.27.3 with golangci-lint/govulncheck/ruff in CI. Evidence: `ci-local.sh all`
PASS (191.7 s), `make web-test` PASS, contract 47 checks / 0 failures, govulncheck 0 reachable.
**Not verified:** the real-engine `engines` phase, a multi-host cluster-lite deployment, GPU
hardware. Details: `docs/IMPLEMENTATION-PLAN.md` → "Review hardening batch"; upgrade notes:
`docs/CHANGELOG.md` → "Unreleased".

## Before you change anything

`AGENTS.md` is the working contract: hard rules (sensitive data, no infrastructure drift, no
unverified claims), the commands that *are* CI (`scripts/ci-local.sh` plus the `web`/`sdk`
jobs, not a reimplementation),
the traps that already cost time, and the deliberate gaps above so they are not "fixed" by
accident.
