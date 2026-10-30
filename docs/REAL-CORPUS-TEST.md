# REAL CORPUS TEST — 958 files through the ParseDock API

> Run 2026-10-30 against the workspace corpus in `test_data/` (1017 files / 4.30 GiB).
> Executed locally, no document left the machine, no content stored in the report.
> Harness: `tests/integration/batch_parse.py` (redacted by default: hashed file
> ids, no text), results in `runtime/testdata-run/` (git-ignored).
>
> Engines: xberg 1.3.10 + docling-rs 2.53.3, `mode=auto`, 4 concurrent workers,
> 128 MiB upload cap, `--max-pages 500`.

## 1. Inventory

| ext | files | size | in allow-list |
| --- | --- | --- | --- |
| .pdf | 554 | 1 066 MB | yes |
| .xlsx | 302 | 1 530 MB | yes |
| .xlsm | 71 | 83 MB | yes (macro-enabled, macros never executed) |
| .docx | 15 | 7 MB | yes |
| .csv | 14 | 0.3 MB | yes |
| **.doc** | **12** | **72 MB** | **no — legacy OLE2, rejected 415** |
| .png | 5 | 1 MB | yes |
| .md / .txt | 3 / 2 | ~0 | yes |
| **.xls** | **2** | **0.1 MB** | **no — legacy OLE2, rejected 415** |
| .pptx | 2 | 2.6 MB | yes |
| .zip | 9 | 1 643 MB | no (archives are not documents) |
| no extension / .svg / .kml | 26 | 0.2 MB | no |
| Office lock files (`~$…`) | 35 | – | skipped |

958 files were eligible (legacy `.doc/.xls` and archives excluded).

## 2. Result (after the fixes in §3)

| ext | files | completed | rate | engine p50 | engine p95 | chars | tables |
| --- | --- | --- | --- | --- | --- | --- | --- |
| .pdf | 554 | 542 | **97.8 %** | 268 ms | 1 365 ms | 22.85 M | 5 106 |
| .xlsx | 294 | 276 | 93.9 % | 378 ms | 1 020 ms | 41 K | 934 |
| .xlsm | 71 | 71 | 100 % | 84 ms | 407 ms | 19 K | 453 |
| .docx | 13 | 13 | 100 % | 33 ms | 88 ms | 313 K | 91 |
| .csv | 14 | 14 | 100 % | < 1 ms | – | 252 | 14 |
| .png | 5 | 5 | 100 % | 255 ms | 491 ms | 3 K | 2 |
| .md / .txt | 3 / 2 | 5 | 100 % | < 1 ms | – | 15 K | 8 |
| .pptx | 2 | 2 | 100 % | 43 ms | 36 ms | 7 K | 0 |
| **total** | **958** | **928** | **96.9 %** | – | – | **23.3 M** | **6 608** |

Wall time 348 s (2.75 files/s on one host, 4 workers). 332 files (36 %) produced
OCR/no-text-layer warnings — they are scans and are the population where OCR
quality matters.

Before the fixes in §3 the same corpus completed 79.2 % (PDF 67.3 %).

## 3. Defects found and fixed (PR #3)

| # | Defect | Effect on the corpus | Fix |
| --- | --- | --- | --- |
| 1 | `CheckPDF` ran the structural-marker check before reading the file tail, while a PDF's `startxref`/`%%EOF` lives at the end | **168 of 554 PDFs falsely rejected** as "markers missing" | read head **and** tail first; `TestCheckPDFFindsTrailerInTail` |
| 2 | Same bug hid `/Encrypt` in the tail | only 2 of 8 encrypted PDFs detected | encryption now detected from head+tail; 8/8 reported as `encrypted_file` |
| 3 | Error classification: 512 MiB expansion cap reported as `suspicious_compression` (a bomb signal); stderr truncated into a WARN line hid "Archive too large"; "exit 0 + truncated JSON" became `internal_error` | 14 legitimate 58–74 MiB ledgers mislabelled; 2 opaque internal errors | new `expanded_too_large` (413) with the limit in the message; `ClassifyCLIError` filters log noise and maps limits/timeout/encryption/unsupported; valid JSON is accepted even when `exec` reports a context error |

Also: a `%PDF-` header within the first 1024 bytes is accepted (Adobe behaviour),
and the deflate-bomb ratio check stays at 200:1 on parts > 1 MiB so legitimate
Office files (10–40×) pass while the synthetic bomb is still rejected.

## 4. Remaining failures are limits, not crashes

| Cause | Code | Count | Notes |
| --- | --- | --- | --- |
| expanded OOXML beyond parser limits | `expanded_too_large` | 14 | 58–74 MiB SAP ledgers, 550–770 MiB expanded; xberg caps at 500 MiB, docling-rs at 10 M cells/sheet |
| encrypted PDF | `encrypted_file` | 8 | password support is out of scope |
| > 500 pages | `page_limit_exceeded` | 1 | 561 pages; raise `--max-pages` per tenant |
| corrupt / zero-padded PDF, empty XLSX | `unsupported_type` / `malformed_file` | 5 | verified: xberg also fails on the same bytes |
| **legacy `.doc` / `.xls`** | `unsupported_type` | **14** | allow-list decision, not an engine limit — both engines can read them (`.xls` verified directly) |

## 5. fast vs accurate on real documents (40 PDFs)

| metric | xberg (`mode=fast`) | docling-rs (`mode=accurate`) |
| --- | --- | --- |
| median engine time | **276 ms** | 4 014 ms (14.5×) |
| completed | 40/40 | 39/40 (one corrupt file rejected by pre-flight) |
| files with >20 % less text | – | 14 of 39 |
| files with more tables | – | 4 of 39 (e.g. 0 → 3 tables) |
| warnings emitted | OCR/scan warnings on scans | none (no page/bbox warnings) |

Conclusion: on this corpus **`fast` is the better default**; `accurate` buys table
structure and CJK-scan OCR at ~14× the latency and sometimes with less text. This
matches the corrected scoring in `ENGINE-DECISION.md` (§2) and is why the router
treats `accurate` as a *structure/provenance preference*, not a quality promise.

## 6. Operational findings

* **Status polling consumes the same per-owner rate budget as uploads.** A batch
  client polling at 5 rps/thread hit `429 rate_limited` with the default
  20 rps/burst 40 (36 files affected in the first attempt). Options: raise
  `--rate-limit` for batch tenants, poll with backoff (the harness now uses 0.5 s),
  or subscribe to SSE instead of polling. Recorded for PR-09 (observability/limits).
* Uploads of 58–74 MiB at 128 MiB cap work, but the default 64 MiB cap rejects
  them: capacity planning must account for real document sizes (`docs/DEPLOYMENT.md`).
* A 4-worker host sustained 2.75 files/s on this corpus (PDF-heavy, mixed sizes);
  engine time p95 for PDFs was 1.4 s, so the pipeline, not the engine, dominated
  wall time.

## 7. Reproduce

```bash
# start a stack with the real engines (see docs/ENVIRONMENT.md)
./runtime/bin/docparse --role standalone --engines native,xberg,docling-rs ... \
  --max-upload-mb 128 --rate-limit 500 --rate-burst 200

python3 tests/integration/batch_parse.py \
  --dir test_data --api http://127.0.0.1:18090 --token "$KEY" \
  --mode auto --concurrency 4 --max-mb 128 \
  --out runtime/testdata-run/auto.json
```

`--no-redact` writes real filenames; do not commit any output from a private corpus.
