# Phase 1 summary

> One page: what shipped, what was verified, and what is still open. Details live in the
> per-PR sections of `docs/IMPLEMENTATION-PLAN.md` and the referenced reports.

## Shipped

| PR | Slice | Status | Evidence |
| --- | --- | --- | --- |
| 01 | CI baseline, reproducible builds | **DONE** | 4 workflows, `scripts/ci-local.sh`, pinned inputs, byte-identical rebuilds; PR runs 3 m 53 s – 4 m 20 s |
| 02 | Public-edge security | **DONE** | hashed keys + scopes, per-owner quota, audit lines, request-id; `pr02_contract.py` 6/6 |
| 03 | Observable cancellation | **DONE** | `cancel_requested_at`, worker discards late results, per-engine ceilings; T17 |
| 04 | Engines | **DONE** | artefact SHA-256 pins, legacy OLE2, scan→accurate escalation, RSS ceiling/OOM code |
| 05 | Document IR v0.1 | **DONE** | published JSON Schema + drift check, golden Docling→IR, XLSX formula enrichment |
| 06 | Storage & quota | **DONE** | transactional `owner_storage`, completion-time quota, atomic retention, gzip downloads |
| 07 | Cluster-lite | **PARTIAL** | mTLS end-to-end (`pr07_tls.py` 8/8), load 8.8 jobs/s at 88 % of the parser ceiling; **two-host + real partition not measured** |
| 08 | GPU worker | **PARTIAL** | image built and verified (`pr08_gpu.py` 4/4: MinerU on GPU, 18.8 GiB free VRAM reported, `gpu_required` completed), VRAM admission at routing **and** claim; **docling-serve CUDA unwired, external distribution blocked by L1** |
| 09 | Observability | **DONE** (no OTel) | dependency-free Prometheus surface, RC-5 throttling aggregation, `SLO.md`, dashboard + 7 alerts |
| 10 | SDKs | **PARTIAL** | installable Python package + CLI, one version across artefacts, Java client builds; **publishing and `schemathesis` not done** |
| 11 | Corpus & quality gate | **PARTIAL** | 110-document reproducible corpus, hash-checked, run-scoped gate (>2 % quality / >15 % latency); **no TEDS/APTED, no public licensed corpus** |
| 12 | Runbooks | **DONE** | online backup (lease-aware, verified), measured restore drill (RTO 0.1 s, 7/7), `RUNBOOK.md` |

## Verified end to end (real processes, real files, real engines where applicable)

* **Real corpus** (re-run on current `main`, `.doc`/`.xls` now actually sent): 972 documents,
  **96.5 % completion**, 23.34 M chars, 6 612 tables; 3.6 files/s at client c=16 with the four
  parser slots 90 % busy (`docs/REAL-CORPUS-TEST.md` §2a). Three defects found in the first
  pass (PDF trailer, `/Encrypt`, `expanded_too_large`) plus two in the re-run (relative
  `--work-dir` broke every CLI parse; the claim poll interval was a hard-coded 1 s).
* **CI**: six blocking PR jobs; a negative-control PR proved the merge gate fails on a
  contract regression.
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
| TEDS/APTED, public licensed corpus, SDK publishing, `schemathesis` | scoped out of PR-10/11 with reasons recorded there |
| Legacy `.doc`/`.xls` from the real corpus | supported by engines and allow-list now; not yet re-run against the corporate corpus after PR-04 |

## How to reproduce the claims

```bash
make ci                # every CI phase locally
make acceptance        # T03–T17
make drill             # backup + restore with RTO
make gpu-check         # GPU image end to end (needs a GPU host)
make gate              # engine quality/latency gate
make sdk-check         # Python + CLI + Java SDK + version consistency
make load              # cluster-lite throughput
```

Sensitive corporate material (`test_data/`) is local-only and never committed; the batch
harness redacts file identities by default.

## Before you change anything

`AGENTS.md` is the working contract: hard rules (sensitive data, no infrastructure drift, no
unverified claims), the command that *is* CI (`scripts/ci-local.sh`, not a reimplementation),
the traps that already cost time, and the deliberate gaps above so they are not "fixed" by
accident.
