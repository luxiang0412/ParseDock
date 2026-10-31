# REAL CORPUS TEST — the corporate corpus through the ParseDock API

> Two runs are recorded. **§2** is the original run (958 eligible files, before legacy Office
> was accepted). **§2a** is the re-run after PR-04/PR-12, with `.doc`/`.xls` actually sent —
> it is the current number.

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

## 2a. Re-run after PR-04/PR-12 (current)

Same corpus, same host, current `main`. The only intentional change is that the batch harness
now sends the legacy OLE2 extensions (`.doc .dot .xls .xlt .ppt .pot`), which the service has
accepted since PR-04 but the harness silently skipped — that alone is why the old run reported
"unsupported.doc: 12".

| ext | files | completed | rate | engine p50 | engine p95 | chars | tables |
| --- | --- | --- | --- | --- | --- | --- | --- |
| .pdf | 554 | 542 | **97.8 %** | 265 ms | 1 361 ms | 22.85 M | 5 106 |
| .xlsx | 294 | 276 | 93.9 % | 381 ms | 1 025 ms | 41 K | 934 |
| .xlsm | 71 | 71 | 100 % | 80 ms | 407 ms | 19 K | 453 |
| .docx | 13 | 13 | 100 % | 26 ms | 64 ms | 313 K | 91 |
| .doc | 12 | **12** | 100 % | 13 ms | 4 s | 102 K | 2 |
| .csv | 14 | 14 | 100 % | < 1 ms | – | 252 | 14 |
| .png | 5 | 5 | 100 % | 258 ms | 489 ms | 3 K | 2 |
| .md / .txt | 3 / 2 | 5 | 100 % | < 1 ms | – | 15 K | 8 |
| .ppt / .pptx | 0 / 2 | 2 | 100 % | 25 ms | 23 ms | 7 K | 0 |
| .xls | 2 | **2** | 100 % | 12 ms | 8 ms | 122 | 4 |
| **total** | **972** | **943** | **97.0 %** | – | – | **23.34 M** | **6 612** |

After two follow-ups — HTML content behind a `.doc` extension is served by the content, and the
page cap went from 500 to 2000 — the run is **943/972 (97.0 %)**: `.doc` is 12/12 (8 real OLE2
via xberg, 4 HTML via docling-rs) and PDF is 98.0 %. The 29 remaining failures are all
pre-parse limits or genuine file problems, not crashes:

| code | n | what it means |
| --- | --- | --- |
| `expanded_too_large` | 16 | the SAP ledgers: 52–74 MiB zipped, 503–609 MiB expanded (14 hit the upload cap, 2 the 256 MiB result ceiling); needs sheet-window extraction |
| `encrypted_file` | 8 | password-protected PDFs |
| `unsupported_type` | 4 | 2 `.xlsx` that are DRM/zero-filled containers and 2 zero-filled PDFs |
| `malformed_file` | 1 | truncated PDF |

**Speed.** Engine time is unchanged (PDF p50 265 ms, p95 1.36 s). Wall time depends on who is
the bottleneck:

| client concurrency | wall | throughput | what limits it |
| --- | --- | --- | --- |
| 4 | 419 s | 2.3 files/s | the *client*: the harness polls every 500 ms, and slot utilisation was only 26 % |
| 8 (after the 250 ms poll interval, PR after the audit) | **266 s** | **3.66 files/s** | the 4 parser slots |
| 16 | 267 s | 3.6 files/s | the 4 parser slots, at **90 % utilisation** (`parsedock_parse_seconds` sum ÷ wall ÷ slots) |

At c=16 the mean claim→commit time was 1.04 s per document, of which the engine itself was
~0.47 s: the rest is input transfer, result upload and the coordinator's fsync-backed commits.
One outlier dominates the tail: an 18-page scanned Spanish PDF (10.8 MB) took **77 s** with
**2.5 GB peak RSS** — OCR-bound, not a leak.

**Stability.** 0 lease expiries, 0 retries, 4 worker-level failures (the rest were rejected
before a worker saw them), coordinator RSS **21–22 MB flat across 639 samples** (~7 min).
`--escalate-scans` fired 6 times; 6 of them were rejected by the "retry produced less text"
guard, so the fast result was kept. Quality totals are unchanged from §2 (23.34 M chars,
6 612 tables).

**Two defects this re-run exposed, both fixed here:**

1. **A relative `--work-dir` broke every CLI-based parse.** Engines run with `cmd.Dir` set to
   the attempt directory while the input path was derived from the (relative) scratch root, so
   on a fresh checkout every PDF/XLSX failed with `File not found: runtime/work/...`. The
   worker now resolves its scratch root to an absolute path once at start-up
   (`TestWorkerResolvesWorkDir`). The acceptance suites never caught it because they always
   pass absolute paths.
2. **The claim poll interval was a hard-coded 1 s with no flag**, so a shallow queue paid up to
   a second of dead time per document: six sequential 3-page PDFs measured
   **1 220 ms → 417 ms** each after the interval became configurable and defaulted to 250 ms
   (`--poll-interval`). With a deep queue the same change raised a mock-engine 4-slot run from
   11.4 to 13.0 jobs/s.

### What is *not* processed, and why (current run)

972 files are eligible (the service accepts the type and size); 938 complete. The other 45
files the harness skips are not documents at all, and the 34 failures are all limits:

| Group | Files | Bytes | What it is | What it would take |
| --- | --- | --- | --- | --- |
| Oversized XLSX | **16** | 1 011 MiB | 52–74 MiB SAP ledgers (expand to 503–609 MiB); 14 hit the upload cap, 2 the 256 MiB result ceiling | sheet-window extraction (deferred by decision) |
| Encrypted PDFs | 8 | 22 MiB | password-protected | a password field on the API (not planned) |
| Not a document | 4 | 4 MiB | 2 `.xlsx` DRM containers, 2 zero-filled PDFs | nothing |
| Skipped by the harness | 45 | 1 643 MiB | 9 **ZIP archives** (1.6 GiB — archives, not documents), 24 no-extension cache blobs (`00 00 00 01`, 0.2 MiB), 10 Office lock files (`~$`), 1 KML, 1 SVG | a zip-bundle feature would be needed for the archives |

So of the 4.4 GiB corpus, the **1.6 GiB share is nine zip archives**, and the next largest
unprocessed group is the 16 oversized spreadsheets.

### Correction: OCR language was pinned to `eng`

This run (and the earlier ones) started the stack with `--xberg-ocr-lang eng`. The corpus is
mostly Chinese, so **the OCR quality numbers for the 332 scanned files understate the engine**:
on the Chinese scanned fixture, `eng` returns garbage while `--xberg-ocr-lang zh` reproduces the
page exactly, and `zh` degrades an English scan in the same way. A combined `chi_sim+eng` value
is rejected by xberg 1.3.10. Treat the scan numbers here as a lower bound; a per-language worker
(or a deployment-level choice) is the correct configuration. See `docs/DEPLOYMENT.md`.

### Still open after this run

* A 10 MB / 1.34M-cell workbook spent 1.7 s in formula enrichment (down from 2.35 s after
  streaming the sheet XML instead of unmarshalling it) and ~4 s in serialising and rendering
  the result. Large-sheet output, not parsing, is the remaining cost.
* Rendered Markdown/text repeated a table's cells: engines emit the table both as page text and
  as a structured `Table`. The worker now drops the redundant lines (`DropTableShadows`), so a
  table appears once, as a Markdown table.
* An 18-page scanned PDF costs 77 s: OCR-bound, and the `xberg` CLI exposes no page/thread
  parallelism, so per-page splitting (our side) or a serve-mode adapter would be needed.
* 4 `.doc` files whose content is not an OLE2 compound file despite the extension.

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
* A 4-worker host sustained 2.3 files/s at client concurrency 4 and 3.6 files/s at 16
  (§2a). The engine is *not* the bottleneck at that scale: at c=16 the four slots ran at 90 %
  utilisation with ~0.47 s of engine time inside a 1.04 s claim→commit window, and at c=4 the
  client's 500 ms polling halved throughput. Optimise the client (SSE, tighter polling) or add
  slots before optimising parsers — see `docs/SLO.md` §4.

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
