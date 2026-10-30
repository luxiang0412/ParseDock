# PHASE 1 READINESS — PR-00 result

> Date: 2026-10-30 · Branch state: `PR-00` on top of Phase 0 baseline `57a08b1`
> Companion: `docs/PR00-ISSUE-MATRIX.md` (per-finding verification).
> Scope: the eleven findings (F01–F11) of the Phase 1 readiness review, plus the
> tests the review asked for (PR00-01…PR00-15). **No PR-01…PR-12 work was done.**

## 0. Verdict

**CONDITIONAL GO for Phase 1 engineering**, with two conditions that remain outside
this codebase:

| Condition | Status | Owner |
| --- | --- | --- |
| C1 — licence decision for `libx265` (L1) before any external distribution of `parsedock-worker-cpu` | **BLOCKED** (legal) | legal + build |
| C2 — GPU worker image + two-host network behaviour verified | **PARTIAL** (PR-08 / PR-07) | platform |

Everything else that PR-00 could verify inside this repository is **PASS** and
reproducible with the commands in §5. The public API, IR and SDK contract are now
internally consistent, so PR-01 (CI) can start immediately and PR-02…PR-12 keep
their scope with the adjustments noted in §6.

## 1. Method (and what was rejected)

1. Read all nine source documents, then **read the code** (`internal/**`, `cmd/**`,
   `api/openapi.yaml`, harnesses) and re-ran the measurements.
2. Every finding was classified as CONFIRMED / PARTIAL / REJECTED with file-level
   evidence in `PR00-ISSUE-MATRIX.md`. Two review claims did not hold:
   * "an external caller can forge `X-Parsedock-Owner` through the API" — the
     gateway already strips both internal headers (`internal/api/server.go`
     `Rewrite`) and the public mux has no `/internal` route. The real gap was that
     the coordinator would happily bind a public address with an **empty** internal
     token; that is now refused.
   * "…so the scores are not comparable" was right, but the data was worse than
     described: the structure metric averaged only samples that produced a table
     (rewarding silence), provenance was a JSON substring test, and the xberg
     adapter counted page numbers as sheet names. All three were fixed in the
     harness before any score was republished.
3. Three additional defects were found that the review did not list and are fixed:
   * `device=auto` preferred GPU engines over higher-priority CPU engines;
   * the Tika adapter fabricated page numbers for PDFs (Tika emits no page
     boundaries for them);
   * the coordinator's text-format shortcut produced an `engine_hint` the worker
     would reject (`native` did not declare `accurate`).

## 2. Findings — result

| ID | Result | What changed | Evidence |
| --- | --- | --- | --- |
| **F01** scoring arithmetic | **FIXED** | `benchmark/scripts/score.py` is the only source of the numbers; docs tables are generated; fixed sample sets, applicability, double-weighted scans, CER clamping, structure scored on every table fixture, unmeasured dimensions `null` + renormalised weights + `incomplete` flag | `make score`, 21 unit tests (`PR00-01`) |
| **F02** `accurate` vs reality | **FIXED** | quality split into text/structure/provenance; text formats always route to `native` in every mode (GB18030 regression); `auto` no longer prefers GPUs; `mode` documented as a preference with measured counter-examples; routing table test | `TestRouteTable`, `TestTextFormatsUseNativeInEveryMode`, PR00-02, PR00-14 |
| **F03** ACK/Cancel/Sync/TTL | **FIXED** | cancel on a completed job requires `result_version`; heartbeat distinguishes `cancelled` from `lease_lost`; `fail` is idempotent for a cancelled attempt; lease creation and the cleaner's `deleting` transition are single conditional statements; `processing_deadline_at` exposed (alias kept); `/v1/parse` documented and tested as one-shot | `TestCancelCompletedRequiresResultVersion`, `TestCancelledAttemptFinalisation`, `TestDownloadLeaseIsAtomicWithRetention`, PR00-03/04/05 |
| **F04** identity boundary | **FIXED (code), PARTIAL (mTLS)** | key→owner rotation proven; API header stripping proven; `/internal` unreachable via the gateway; coordinator refuses a non-loopback internal bind without a token. mTLS remains PR-07 | `TestValidateInternalBinding`, PR00-10 |
| **F05** ETag vs ACK version | **FIXED** | per-artifact strong `ETag` (`sha256` of the representation), `If-None-Match`→`304`, `Vary: Accept-Encoding`; `X-Result-Version` stays the manifest hash and the only ACK token; SDK no longer falls back to ETag | PR00-06, contract checks |
| **F06** OpenAPI 3.1 / split / multipart order | **FIXED** | zero `nullable:`; public spec has no `/internal`, no internal scheme, no worker schemas; internal protocol moved to `api/openapi.internal.yaml`; SDKs regenerated from the public spec only; multipart parts accepted in **any order** (streamed to staging, no memory buffering) | 29 contract checks (`PR00-08`), PR00-09 |
| **F07** SSE scope / replay / drift | **FIXED** | explicit `scope=processing|retention`; `snapshot` first; `cursor_reset` when the cursor predates the retained window; 10-minute stream cap; worker heartbeat endpoint now in the internal spec | PR00-07, contract drift check |
| **F08** IR boundaries | **FIXED (partly BLOCKED)** | Tika page numbers no longer fabricated (+`no_page_boundaries`, page count from `xmpTPg:NPages`, markdown lifting for OOXML); `page_width_pt`/`page_height_pt` populated from Docling; `image_ref` pinned as reserved-and-unused; formula text documented as an engine capability gap | 5 engine tests, `TestDoclingPageDimensions`, `TestNoAdapterEmitsDanglingImageRefs` (`PR00-11`) |
| **F09** worker scratch | **FIXED** | per-attempt cleanup + startup sweep + periodic scrub + `--work-max-age`; live attempts immune | `TestSweepScratch*`, PR00-12 |
| **F10** SLO / backup semantics | **FIXED** | SLO restated for committed/not-expired/not-ACKed results; coordinator disk loss named as a single point; offline `scripts/backup.sh`/`restore.sh` back up **only active** artifacts, restore purges non-active dirs; test proves a released result does not come back | PR00-13 |
| **F11** licence gate | **ENGINEERING DONE, LEGAL BLOCKED** | `scripts/license_audit.sh` builds `build/sbom/<image>.json` (dpkg inventory, GPL/LGPL findings, notices) and fails on GPL; control-plane image PASS, worker image **BLOCK** (`libx265`); legal decision L1 explicitly not taken | PR00-15 (`make license-audit`) |

## 3. Test evidence (all commands re-run for this report)

| Suite | Command | Result |
| --- | --- | --- |
| Go unit + integration | `make test` (`go vet`, `go test ./...`) | **PASS** — 41 test functions across storage/IR/engines/coordinator/worker |
| Scoring rubric | `python3 tests/unit/test_score.py` | **PASS** — 21 tests (PR00-01) |
| Contract / drift | `python3 tests/contract/openapi_contract.py` | **PASS** — 29 checks (PR00-08) |
| PR-00 runtime | `python3 tests/integration/pr00_contract.py` | **PASS** — 10/10 (PR00-02…07, 09, 10, 12, 14) |
| Backup/restore | `python3 tests/integration/backup_restore.py` | **PASS** (PR00-13) |
| Acceptance lifecycle | `make acceptance` | **PASS** — T03–T16, 13/13 |
| Acceptance real engines | `make acceptance-real` | **PASS** — T01/T02/T11, 3/3 |
| Python SDK lifecycle | `make sdk-test` | **PASS** — 9 checks incl. failing-sink and completed-cancel |
| Container smoke (CPU compose) | compose up, `mode=accurate` on GB18030 CSV | **PASS** — `engine=native`, correct 区域 header, cross-replica download, `ETag`/304 |
| Licence gate | `make license-audit` | control plane **PASS**; worker image **BLOCK** (`libx265`) — expected |

Raw artefacts: `benchmark/results/raw/pr00-contract-*.json`,
`pr00-backup-*.json`, `acceptance-lifecycle-*.json`, `acceptance-real-*.json`,
`benchmark/results/summary/scores.json`, `build/sbom/*.json`.

## 4. Compatibility and migration

The service is pre-1.0; the following are **intentional** behaviour changes and are
documented in `api/openapi.yaml`:

| Change | Impact | Migration |
| --- | --- | --- |
| `POST /cancel` on a completed job needs `result_version` | previously released without proof | send the version (or call `/ack`); the SDK accepts it as an argument |
| `ETag` value changed to the per-artifact hash | clients that cached the old ETag re-download once | expected; caches recover on the first `200` |
| `processing_deadline_at` added, `max_lifetime_at` deprecated | additive | keep reading the old field until Phase 1 ends |
| `mode=accurate` on text formats now returns native output | more correct, different engine | none; IR shape unchanged |
| SSE default scope ends at the processing terminal state | unchanged behaviour, now documented | use `scope=retention` if release events are needed |
| Multipart parts may arrive in any order | previously only file-first worked | none (old clients keep working) |
| Coordinator refuses a public internal bind without a token | deployment that omitted the token must set it | set `PARSEDOCK_INTERNAL_TOKEN` |
| `POST /internal/diagnostics/workers` moved off the public mux | operators must call the internal listener | internal tooling only |

## 5. Reproduce

```bash
make build
make test            # Go + scoring tests
make contract        # OpenAPI/SDK/router agreement
make pr00            # 10 runtime scenarios + backup/restore
make acceptance      # T03–T16
make acceptance-real # T01/T02/T11 (needs xberg + docling-rs installed)
make sdk-test
make images && make up   # CPU compose stack
make license-audit       # SBOM + GPL gate (expects worker BLOCK until L1)
```

## 6. Remaining risks and what is explicitly NOT done

| # | Item | Status | Gate/owner |
| --- | --- | --- | --- |
| R1 | `libx265` (GPL-2.0) inside the xberg bundle | **BLOCKED** | legal (L1) — external distribution must not ship the current worker image |
| R2 | MinerU attribution + flash-only enforcement | open (code enforces flash, egress policy not yet automated) | PR-08 |
| R3 | GPU worker image and GPU container acceptance | **PARTIAL** — native CUDA measured, image not built | PR-08 |
| R4 | Real two-host cluster-lite + network partition + load | **PARTIAL** — same-host only | PR-07 |
| R5 | Certificate/mTLS between components | not implemented (shared token only, with a bind guard) | PR-07 |
| R6 | Formula text (`Cell.Formula`) in XLSX | **BLOCKED on engine capability**; documented, asserted empty | PR-05 |
| R7 | Image artifacts (`image_ref` + download endpoint) | reserved, pinned unused | PR-05 |
| R8 | Corpus size (24 synthetic files) | unchanged; scores marked low-confidence | PR-11 |
| R9 | OCR quality on real scans, online backup, per-tenant disk quota | open | PR-06/PR-11 |
| R10 | `delete`/`expired` tombstone edge under backup + restore of a *pre-ACK* backup | the scripts exclude non-active artifacts, but a backup taken before an ACK will restore an active result — that is correct (the ACK happened after the backup) and must be documented in the restore runbook | PR-12 |

**Not implemented on purpose** (out of PR-00 scope): PR-01…PR-12 features, mTLS,
GPU image build, corpus expansion, per-owner quotas, Prometheus metrics, webhooks,
URL ingestion, and any change to the storage/queue architecture.

## 7. Recommendation

1. **Proceed with PR-01 (CI)** now — the contract is stable and the checks below are
   ready to become blocking CI jobs: `make vet test contract pr00`, plus
   `make acceptance` on PRs and `make acceptance-real` nightly.
2. Run **PR-02** (key hashing/rotation/quotas) next; it builds directly on the
   identity boundary verified here.
3. Start **L1** in parallel with legal. Until it is signed, publish only the control
   plane image externally (it passes the gate) and treat the CPU worker image as
   internal-only.
4. Keep the "one-shot vs ACK" distinction and the three-way quality score in the
   frozen contract; they are now the parts most likely to be silently broken later.
