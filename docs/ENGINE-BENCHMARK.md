# ENGINE BENCHMARK — Phase 0

> Reproduction: `make bench` (`benchmark/scripts/bench.py`) on the host in
> `docs/ENVIRONMENT.md`. Raw records: `benchmark/results/raw/bench-*.jsonl`.
> Summary tables: `benchmark/results/summary/benchmarks-{quality,perf,concurrency}.csv`
> (rebuilt by `benchmark/scripts/summarize.py`).
>
> **Discipline:** fixed fixture hashes, pinned engine versions, config hash per
> engine, warm-up separated from measured runs, 5 repeats, median reported,
> xberg's on-disk result cache **disabled** (otherwise repeated runs are cache hits
> and the numbers are meaningless). Two measurement mistakes were found and fixed
> before publishing: `%e` resolution (10 ms) and the xberg cache. Both flawed runs
> are still in `results/raw/` for audit.

## 1. Corpus

24 synthetic, self-authored fixtures (`benchmark/fixtures`, SHA-256 in
`manifest.json`), each with gold text for scoring:

| Group | Fixtures |
| --- | --- |
| native PDF | `pdf_text_en_3p`, `pdf_text_zh_2p`, `pdf_two_column_en`, `pdf_text_en_30p` |
| scanned | `pdf_scanned_en`, `pdf_scanned_zh`, `scan_source_en.png`, `scan_source_zh.png` |
| Office | `docx_report` (headings, bullets, table, header), `xlsx_workbook` (2 sheets, formulas, merged cell, dates, float) |
| text | `csv_utf8`, `csv_gb18030`, `csv_semicolon_eu`, `tsv_data`, `txt_plain`, `md_notes` |
| images | `image_screenshot.jpg/.webp` |
| adversarial | zero-byte, corrupt PDF, renamed DOCX, encrypted PDF, zip bomb, fake zip |

This is a **smoke-scale** corpus (24 files), not the 120–200 document corpus the
task guide prescribes for the final selection. It is enough to expose format and
architecture differences and to build the harness; PR-11 in
`docs/IMPLEMENTATION-PLAN.md` covers the full corpus.

## 2. Quality

`CER`/`WER` are computed on normalised text against the gold file (prefix-capped at
40 000 chars). `table` is positional cell accuracy against the known 5×4 fixture
table (whitespace/comma-insensitive), read from the engine's structured output —
not from its Markdown.

| engine | en PDF (3 p) | zh PDF (2 p) | two-column | scan EN | scan ZH | CJK image | docx | xlsx |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| xberg | CER 0.116 / tbl 1.00 | 0.440 / 1.00 | 0.287 | 0.055 | 0.824 | 0.843 | 0.082 / 1.00 | 0.025 |
| docling-rs | **0.007** / 1.00 | **0.034** / 1.00 | 0.277 | 0.884 | **0.098** | **0.098** | 0.284 / 1.00 | 0.254 |
| docling-serve | **0.006** / 1.00 | **0.034** / 1.00 | 0.277 | 1.000¹ | 1.000¹ | 1.000¹ | 0.284 / 1.00 | 0.254 |
| MinerU (flash) | 0.167 / 1.00 | 0.628 / 1.00 | 0.276 | 0.664 | 0.451 | 0.451 | –² | –² |
| Tika | 0.123 | 0.510 | 0.296 | 0.199 | 1.255³ | 0.941 | **0.045** / 1.00 | 0.314 |
| native | – | – | – | – | – | – | – | – |
| native (text formats) | csv UTF-8 **0.000**, csv GB18030 **0.000**, tsv 0.000, md 0.000, txt 0.000 | | | | | | | |

¹ docling-serve ran with OCR disabled (its default): image-only inputs return no text.
² MinerU only accepts PDF/images.
³ Tika splits PDF text on form feeds and returned the page twice in this fixture
(CER > 1 means insertions dominate) — a real behaviour, not a harness bug.

Reading order: docling-rs/docling-serve follow the document body tree and handle
the two-column fixture's left-then-right order; xberg, Tika and MinerU emit
text order and get 0.28–0.30 CER on that fixture because the interleaving differs.

Structural fidelity (`has_bbox`, sheet names):

| engine | bbox per element | table cells + spans | sheet names | heading levels | element confidence |
| --- | --- | --- | --- | --- | --- |
| docling-rs / docling-serve | ✅ | ✅ | ✅ | ✅ | ❌ |
| xberg | table bbox only | ✅ | ❌ (warning) | ⚠️ via style annotations | document-level quality only |
| MinerU flash | ❌ | ✅ | ❌ | ⚠️ from markdown | ❌ |
| Tika | ❌ | ❌ | ❌ | ❌ | ❌ |
| native | ❌ | ✅ (csv/tsv) | pseudo-sheet | ✅ (markdown) | ❌ |

## 3. Latency (warm median / p95, 5 runs, single worker, CPU)

| engine | en PDF 3 p | zh PDF 2 p | two-column | scan EN | scan ZH | CJK image | docx | xlsx |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| xberg | **18.2 / 22.8 ms** | **11.6 / 11.8 ms** | **9.4 / 9.9 ms** | **222.8 / 225.9 ms** | **217.1 / 223.1 ms** | 703.9 / 705.1 ms | **25.6 / 27.5 ms** | **21.1 / 22.3 ms** |
| docling-rs | 818.4 / 819.8 ms | 745.6 / 760.9 ms | 277.0 / 278.2 ms | 677.2 / 679.4 ms | 719.1 / 744.0 ms | 1046.4 / 1049.7 ms | 18.6 / 19.1 ms | 16.0 / 18.8 ms |
| docling-serve | 2006.9 / 2007.1 ms | 2010.7 / 2010.7 ms | 2008.8 / 2009.0 ms | 2009.3 / 2009.5 ms | 2009.7 / 2009.7 ms | 2009.1 / 2009.1 ms | 2005.6 / 2007.3 ms | 2010.1 / 2010.3 ms |
| MinerU flash | 928.0 / 928.9 ms | 946.2 / 949.9 ms | 916.4 / 929.8 ms | 868.7 / 876.3 ms | 861.7 / 870.9 ms | – | – | – |
| Tika | 9.8 / 12.6 ms | 8.2 / 8.6 ms | 4.6 / 4.9 ms | 342.3 / 347.4 ms | 329.2 / 329.4 ms | 218.7 / 224.6 ms | 95.6 / 95.8 ms | 6.4 / 6.4 ms |
| native (csv/tsv/txt/md) | < 1 ms (measured 0 ms at 10 ms timer resolution, sub-ms with `perf_counter`) | | | | | | | |

Cold vs warm: process-per-document engines pay their start-up every time, which is
why "cold" ≈ "warm" for xberg (12–16 ms) and docling-rs (278–815 ms — the warm gap
is model loading from page cache). docling-serve pays a **constant ≈ 2.0 s** per
`/v1/convert/file` call regardless of document size, which dominates everything
below ~1 s of actual parsing; the parse itself took 0.81 s in the server log for
the 3-page PDF.

## 4. Resources

| engine | median RSS | peak RSS | how measured |
| --- | --- | --- | --- |
| xberg | 38.8 MB | 232 MB (CJK OCR) | `/usr/bin/time -f %M` per subprocess |
| docling-rs | 818 MB (PDF) / 20 MB (Office) | 1 167 MB (CJK image) | same |
| MinerU flash | 54 MB (CLI) | + ≈ 880 MB VRAM in the daemon | `/usr/bin/time` + `nvidia-smi` |
| Tika | – | 1.37 GB image, JVM heap not isolated per request | image size + container |
| native | process baseline | – | in-process |
| docling-serve | – | 7.2 GB Python env; server RSS not sampled per request | venv size |

Disk: images 94.8 MB (control plane), 1.19 GB (CPU worker with models), 1.37 GB (Tika).

## 5. Throughput (docs/s, disjoint processes/requests, loopback)

| engine / doc | c=1 | c=2 | c=4 | c=8 |
| --- | --- | --- | --- | --- |
| xberg / 3-p native PDF | 28.2 | 79.4 | **245.9** | 234.9 |
| xberg / 1-p scan | 4.5 | 8.6 | 16.1 | 26.1 |
| docling-rs / 3-p native PDF | 1.2 | 2.0 | 2.7 | 3.1 |
| docling-rs / 1-p scan | 1.5 | 2.2 | 2.8 | 3.3 |
| MinerU / 3-p native PDF | 1.1 | 1.9 | 3.5 | 5.8 |
| Tika / 3-p native PDF | 81.5¹ | 2.1¹ | 4.1¹ | 256.6¹ |
| docling-serve / 3-p PDF | 0.5 | 1.0 | 2.0 | 3.9 |

¹ Tika's numbers are bimodal: 5–10 ms when the parser is warm, 300–500 ms when a
new parser/Tesseract instance is initialised inside the request. The sweep mixes
both states, so it is reported but not used for scoring. This is itself a finding:
Tika's latency is not predictable under bursty load without pre-warming.

docling-serve scaling 0.5 → 3.9 docs/s at c=8 shows the service parallelises across
requests (async job execution), while its per-request latency stays ~2 s.

## 6. Stability / failure behaviour

| Observation | Engine | Detail |
| --- | --- | --- |
| Corrupt/garbage PDF | xberg, docling-rs | clean non-zero exit, error text; ParseDock maps to `malformed_file` |
| Encrypted PDF | all | rejected earlier by ParseDock pre-flight (`encrypted_file`) |
| Zip bomb (40 MiB deflate) | **Tika** | **accepted in 14 ms** (HTTP 200) — Tika alone is not a resource guard |
| Zip bomb | xberg, docling-rs | rejected by ParseDock's archive check before the engine runs |
| GB18030 CSV | **xberg** | mojibake; native engine decodes correctly |
| XLSX sheet names | xberg | not exposed → warning, no fabrication |
| PNG input | MinerU | `--pages` flag rejected for images (adapter fixed to omit it) |
| 30-page PDF | all | completed; docling-rs 2.15 s, MinerU 1.55 s, xberg 53 ms, Tika 33 ms |
| Memory | docling-rs | peaks above 1.1 GB on OCR of a large image → worker needs a memory ceiling |

No engine crashed the worker: all are subprocesses/HTTP calls with timeouts, and
every failure produced a classified error code rather than a lost task.

## 7. Scoring (rubric fixed before the numbers were known)

Weights from the task guide: **quality 40, performance 25, resources 15, format
coverage 10, ops/licence 10**. Each dimension is 0–10, computed as:

* **Quality** — `8 × (1 − mean CER over the six core documents, scans double-weighted)
  + 2 × table accuracy (0 if the engine emits no structure).
* **Performance** — from p95 latency on the 3-page native PDF and the 1-page scan:
  ≥ 100 docs/s → 10, 30 → 8, 10 → 6, 3 → 4, 1 → 2, < 1 → 1.
* **Resources** — peak RSS: < 100 MB → 10, < 300 → 8, < 800 → 6, < 1.5 GB → 4, else 2;
  a resident daemon (> 500 MB VRAM) costs one point.
* **Coverage** — share of the 11 target formats handled with structure.
* **Ops/licence** — installability, single artefact, licence clarity (open items
  from `LICENSE-REVIEW.md`), moving tags.

Result (see `docs/ENGINE-DECISION.md` for the arithmetic):

| engine | quality | performance | resources | coverage | ops/licence | **weighted** |
| --- | --- | --- | --- | --- | --- | --- |
| docling-rs | 9.4 | 3.0 | 6.0 | 9.0 | 9.0 | **7.4** |
| xberg | 6.8 | 9.5 | 10.0 | 9.5 | 6.0 | **8.1** |
| docling-serve | 9.3 | 1.5 | 4.0 | 8.0 | 8.0 | **6.3** |
| MinerU flash | 4.6 | 5.0 | 7.0 | 4.0 | 5.0 | **5.1** |
| Tika | 4.9 | 7.0 | 6.0 | 10.0 | 7.0 | **6.5** |
| native | (n/a for PDF) | 10.0 | 10.0 | 4.0 | 10.0 | specialised |

The scores confirm the split routing rather than a single winner: xberg wins on
speed/weight, docling-rs on structure/accuracy, and neither is adequate alone.

## 8. What is not measured yet

* Documents > 30 pages, > 10 MB, or > 500 pages (limits are enforced, not measured).
* Concurrent *mixed* workloads with real engine selection under queue pressure.
* TEDS/APTED for table structure (only cell accuracy is measured).
* Memory ceilings per engine (`ulimit`/cgroup) and OOM behaviour.
* Long-run stability (> 1 h soak), fd/memory leaks.
* The full 120–200 document corpus with human-checked gold (PR-11).
