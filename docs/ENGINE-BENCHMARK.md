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
| Tika | 0.123 | 0.510 | 0.296 | 0.199 | 1.255³ | 0.941 | **0.045** / 1.00 | 0.314⁴ |
| native | – | – | – | – | – | – | – | – |
| native (text formats) | csv UTF-8 **0.000**, csv GB18030 **0.000**, tsv 0.000, md 0.000, txt 0.000 | | | | | | | |

¹ docling-serve ran with OCR disabled (its default): image-only inputs return no text.
² MinerU only accepts PDF/images.
³ Tika returned the page twice in this fixture (CER > 1 means insertions
dominate) — a real behaviour, not a harness bug. Tika 4.x also emits **markdown**
(headings + pipe tables) for OOXML, which is why its DOCX table score is 1.00 while
its PDF output has no page boundaries at all (verified: 0 form feeds on a 3-page
file; the adapter now omits page numbers instead of inventing them).
⁴ XLSX: Tika returns a flat text dump without table structure.

Reading order: docling-rs/docling-serve follow the document body tree and handle
the two-column fixture's left-then-right order; xberg, Tika and MinerU emit
text order and get 0.28–0.30 CER on that fixture because the interleaving differs.

Structural fidelity (`has_bbox`, sheet names):

| engine | bbox per element | table cells + spans | sheet names | heading levels | element confidence |
| --- | --- | --- | --- | --- | --- |
| docling-rs / docling-serve | ✅ | ✅ | ✅ | ✅ | ❌ |
| xberg | table bbox only | ✅ | ❌ (warning) | ⚠️ via style annotations | document-level quality only |
| MinerU flash | ❌ | ✅ | ❌ | ⚠️ from markdown | ❌ |
| Tika | ❌ | ❌ | ❌ | ⚠️ markdown headings for OOXML only | ⚠️ markdown pipe tables for OOXML only |
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

## 7. Scoring (generated, not hand-computed)

> PR-00 / F01: this section and `ENGINE-DECISION.md` §2 are produced by
> `benchmark/scripts/score.py` from `benchmark/results/summary/*.csv`. Do not edit
> the tables by hand — change the rubric or the data and re-run:
>
> ```
> python benchmark/scripts/score.py --write      # also writes summary/scores.json
> python tests/unit/test_score.py                # rubric/maths regression tests
> ```

Rubric: weights **quality 0.40, performance 0.25, resources 0.15, coverage 0.10,
ops/licence 0.10**. Quality is not one number any more (F02): it is a weighted
composite of **text fidelity** (CER, 0.60), **structure fidelity** (table cells +
headers on every fixture that has a table, 0.25) and **provenance fidelity**
(page/bbox/sheet signals the sample can carry, 0.15).

Measurement rules that were wrong in the Phase 0 report and are now enforced by
the script:

* samples are a **fixed per-domain set** (PDF/Office vs text), so a text engine is
  not scored against PDFs it cannot read;
* missing samples are **excluded and reported**, never counted as 0 (and a score
  from < 3 samples is flagged `low_confidence`);
* **structure is scored on every fixture with a table**; emitting nothing scores 0
  (the Phase 0 script averaged only the samples that produced a table);
* **provenance is extracted per engine** (Xberg: table bbox; Docling: element
  `prov.bbox`; Tika: page boundaries only when form feeds exist) instead of a
  `"bbox" in json` substring test, and the xberg adapter no longer reports page
  numbers as sheet names;
* **CER is clamped to [0, 1]** (Tika's duplicated page produced CER > 1);
* unmeasured dimensions stay `null`: the weighted total renormalises over the
  measured ones and the row is flagged `incomplete` (Phase 0 filled RSS with 0).

#### Quality: three separate fidelities


| engine | domain | text fidelity | structure fidelity | provenance fidelity | **quality** | samples | flags |
| --- | --- | --- | --- | --- | --- | --- | --- |
| native | text | 10.00 | 10.00 | 10.00 | **10.00** | 5/5 | incomplete |
| xberg | pdf_office | 6.99 | 9.72 | 1.43 | **6.84** | 7/7 | — |
| tika | pdf_office | 5.91 | 2.50 | 0.00 | **4.17** | 7/7 | incomplete |
| docling-rs | pdf_office | 6.87 | 9.72 | 4.29 | **7.19** | 7/7 | — |
| mineru | pdf_office | 5.28 | 10.00 | 0.00 | **5.67** | 5/7 | — |
| docling-serve | pdf_office | 4.61 | 9.72 | 2.86 | **5.62** | 7/7 | incomplete |


#### Weighted totals (weights renormalised over measured dimensions)


| engine | quality (40%) | performance (25%) | resources (15%) | coverage (10%) | ops/licence (10%) | **weighted** | measured |
| --- | --- | --- | --- | --- | --- | --- | --- |
| native | 10.00 | 10.00 | n/a | 3.64 | 10.00 | **9.25** | 4/5 |
| xberg | 6.84 | 10.00 | 8.00 | 10.00 | 6.00 | **8.04** | 5/5 |
| tika | 4.17 | 10.00 | n/a | 10.00 | 7.00 | **6.90** | 4/5 |
| docling-rs | 7.19 | 5.00 | 4.00 | 8.18 | 9.00 | **6.45** | 5/5 |
| mineru | 5.67 | 5.00 | 9.00 | 3.64 | 5.00 | **5.73** | 5/5 |
| docling-serve | 5.62 | 2.00 | n/a | 7.27 | 8.00 | **5.03** | 4/5 |

Reading the table: xberg wins on performance/resources/coverage, docling-rs wins on
provenance and near-parity text fidelity, Tika is fast but structurally weak and
its RSS is unmeasured, MinerU is GPU-only and PDF-only, `native` is a specialised
text engine (it cannot read PDFs, hence coverage 3.64) — which is exactly why the
architecture routes instead of picking one engine.

## 8. What is not measured yet

* Documents > 30 pages, > 10 MB, or > 500 pages (limits are enforced, not measured).
* Concurrent *mixed* workloads with real engine selection under queue pressure.
* TEDS/APTED for table structure (only cell accuracy is measured).
* Memory ceilings per engine (`ulimit`/cgroup) and OOM behaviour.
* Long-run stability (> 1 h soak), fd/memory leaks.
* The full 120–200 document corpus with human-checked gold (PR-11).


## Corpus and quality gate (PR-11)

Two corpora, two jobs:

| Corpus | Size | Purpose | Scored? |
| --- | --- | --- | --- |
| `benchmark/fixtures/generated/` | 21 hand-checked documents | quality (CER, table cells, provenance) | yes — this is the score |
| `benchmark/fixtures/corpus/` | **110 synthetic documents** (63 PDF, 16 CSV, 16 TXT, 8 XLSX, 7 DOCX) | throughput, robustness, sheet/table shapes, encoding variety | no — coverage and latency only |

Both are synthetic and reproducible: `make fixtures` regenerates them
(`gen_fixtures.py --corpus 110`), and `tests/unit/test_fixtures.py` fails on any size or
SHA-256 drift, so a silently edited corpus cannot masquerade as an engine improvement.
`corpus/` deliberately varies page counts (1–30), languages (EN/ZH), delimiters (,/;),
encodings (UTF-8/GB18030), table dimensions and workbook shapes; it is not hand-checked and
must not be used to claim quality numbers.

### The gate

`benchmark/scripts/gate.py` (committed baseline in `benchmark/results/baseline.json`)
compares the current summary with the baseline and fails when, for any engine the baseline
covers:

* **quality** or **structure** drops more than **2 % relative**, or
* a per-file **CER** rises by more than 2 points, or
* **p50/p95 latency** regresses more than **15 %**.

```bash
make gate            # check (runs in the nightly/engine-changes pipeline)
make gate-baseline   # record a new baseline — belongs in the PR that changed quality
```

The gate runs on a **run-scoped** summary: `phase_engines` sets
`PARSEDOCK_BENCH_RAW`/`PARSEDOCK_BENCH_SUMMARY` to `build/ci/bench-{raw,summary}`, so
`summarize.py` cannot pick up a successful record left by an earlier run. The committed
baseline records the **scope** the CI job actually measures (`native,xberg,docling-rs` on
`pdf_native_en`, `pdf_scanned_en`, `docx`, `csv`); a baseline-covered sample missing from the
current run is a failure unless `--allow-missing` is passed for a deliberate partial-engine
run. `tests/unit/test_gate.py` covers pass, quality drop, latency regression, median-only
regression, millisecond noise, missing sample (with and without `--allow-missing`) and the
missing-baseline case. The rule is relative on purpose: absolute numbers differ per host,
relative drift does not. A deliberate quality/latency change is allowed, but it must update
the baseline in the same PR and be justified in `docs/DECISIONS.md`.

### Honest limits

* No TEDS/APTED tree-edit distance yet: table quality is measured as cell accuracy + header
  accuracy (`table_cell_accuracy`, `table_header_accuracy` in `summarize.py`), which is
  cheaper to compute and already discriminates the engines. Tree-edit scoring would need a
  gold table structure per fixture.
* Gold text is synthetic; the real corporate corpus (`docs/REAL-CORPUS-TEST.md`) validates
  completion and failure classification, not CER.
* The gate runs where real engines are installed (nightly/engine-changes), not on every PR.
