# ENGINE CANDIDATES — Phase 0 findings

> Every statement below comes from an artifact that was installed and executed on
> the test host on 2026-10-30, not from vendor documentation. Raw runs:
> `benchmark/results/raw/bench-*.jsonl`, `benchmark/results/summary/benchmarks.csv`.
> Licenses: `docs/LICENSE-REVIEW.md`. Per-engine numbers: `docs/ENGINE-BENCHMARK.md`.

## 0. Matrix

| Engine | Version | Install path | CPU | GPU | Formats exercised | Joined ParseDock as |
| --- | --- | --- | --- | --- | --- | --- |
| native (Go) | `go-native-1` | in-process | yes | – | txt, md, csv, tsv | production candidate (fast path) |
| xberg | 1.3.10 | prebuilt CLI, subprocess | yes | no (tesseract CPU) | pdf, docx, xlsx, csv, png, jpg | production candidate (fast path) |
| docling-rs | 2.53.3 | prebuilt CLI, subprocess | yes | no (ONNX Runtime CPU build) | pdf, docx, xlsx, png | production candidate (accurate path) |
| Docling (Python) | 2.145.0 | venv + CLI | yes | yes (CUDA) | pdf, docx, xlsx, png | reference / conformance oracle |
| docling-serve | 1.38.2 | venv + HTTP service | yes | configurable (not wired in Phase 0) | pdf, docx, xlsx, md | optional accurate service |
| MinerU | 4.1.2 | venv + local daemon | yes | yes (used) | pdf, png | optional GPU path (flash tier only) |
| Apache Tika | 4.2.0 | Docker `latest-full` | yes | – | pdf, docx, xlsx, csv, png | fallback / coverage backstop |

## 1. Native (pure Go)

* **What it is:** ParseDock's own extractor for `txt/md/csv/tsv`. UTF-8/UTF-8-BOM/UTF-16/GB18030
  detection, delimiter sniffing (`, ; \t |`), Markdown lifting (headings, lists, tables, code, images).
* **Measured:** < 1 ms per file up to several MB; no subprocess, no model, RSS ≈ process baseline.
* **Correctness win found:** xberg returned **mojibake** for a GB18030 CSV header while the
  native engine decoded it correctly. Text formats must not be routed to an ML engine.
* **Limitation:** no PDF/Office support by design.
* **Route:** always used for `txt/md/csv/tsv` in `mode=fast`, and preferred over any engine for those formats.

## 2. xberg 1.3.10

* **What it is:** Rust core with native PDF text extraction, OOXML parsing, table detection and
  optional OCR backends (tesseract, PaddleOCR, VLM). CLI + HTTP + bindings.
* **Strengths observed**
  * Very fast on digitally-born PDFs: **27 ms** for a 3-page PDF including process start.
  * Returns page-split text, table grids with **bounding boxes** and a quality score.
  * DOCX style annotations (`style_name: heading 1`) can be converted into real heading levels.
* **Weaknesses observed**
  * GB18030 CSV decoded as mojibake (see above).
  * XLSX output is markdown-per-sheet but **sheet names are not exposed** (only sheet index);
    ParseDock records a `sheet_name_unavailable` warning instead of inventing names.
  * OCR quality on the synthetic scan is poor (`dack-ops-q3`, `18,420.60` instead of `18,420.50`),
    CER ≈ 0.12–0.20 depending on engine setting; not a solution for CJK scans.
  * Prebuilt CLI requires **glibc ≥ 2.38** (bookworm's 2.36 fails) and bundles `libx265`
    (GPL-2.0) — see `LICENSE-REVIEW.md` items L1.
  * Caps its thread budget at `min(cpu, 8)` unless configured.
* **Verdict:** keep as the **fast** engine for PDF/DOCX/XLSX/PPTX and images (OCR best-effort).

## 3. docling.rs 2.53.3

* **What it is:** Rust re-implementation of the Docling PDF pipeline (layout model + TableFormer +
  PP-OCR) emitting a `DoclingDocument`.
* **Strengths observed**
  * Full structure: reading order from `body.children`, per-element `prov` with `page_no` and
    `bbox` + `coord_origin`, table cells with `row_span/col_span`, XLSX **sheet names**
    (`groups[label=sheet].name`).
  * Native-text CER is the best measured on the English PDF (0.007) and good on Chinese (0.034).
  * DOCX/XLSX handled without models in tens of ms.
* **Weaknesses observed**
  * Needs 531 MiB of ONNX models; PDF/image path is ~0.7–0.8 s for 3 pages on CPU (≈ 25× xberg).
  * No GPU in the prebuilt CLI (ONNX Runtime CPU only). GPU would require a different build/EP.
  * OCR on the synthetic English scan: CER 0.884 — the OCR recogniser struggles with the
    200 dpi synthetic render even though the same recogniser reads the PNG source well (CER 0.098).
  * `--text-layer-only` mode loses tables/reading order (documented, and reflected as a warning).
* **Verdict:** keep as the **accurate** CPU engine and the primary structured IR source.

## 4. Docling (Python) 2.145.0 and docling-serve 1.38.2

* **What it is:** the reference Docling implementation and its HTTP service.
* **Strengths:** highest-fidelity DoclingDocument, CUDA acceleration works (torch
  2.15.0+cu132 sees the RTX 2080 Ti), rich format list.
* **Weaknesses observed**
  * 7.2 GB virtualenv (torch + CUDA libs); cold model load ≈ 5–6 s per CLI invocation.
  * docling-serve adds a **fixed ≈ 2 s** per `/v1/convert/file` call on this host (job polling),
    independent of document size — significant for small documents.
  * OCR off by default: scanned inputs return empty text (measured CER 1.0) unless `do_ocr=true`.
  * Its own job/task API is **not** used as the public contract (ParseDock keeps its own job model);
    only `/v1/convert/file` is used by the adapter.
* **Verdict:** docling-serve is a viable *accurate* backend where Python is already operated;
  docling-rs covers the same quality class with a 1/7 footprint. Keep Python Docling as the
  **conformance oracle** for IR regressions.

## 5. MinerU 4.1.2

* **What it is:** heavy document pipeline with tiers. Phase 0 verified the 4.1 CLI.
* **Critical findings**
  * Only the **`flash` tier runs locally**. `basic/standard/advanced` are served by
    `mineru.net` — i.e. they require uploading customer documents to a third party.
    The adapter hard-codes `--tier flash` and never uses `--remote`.
  * The CLI requires a **local daemon** (`mineru server start`) and maintains a document
    library under `MINERU_HOME`; this is an extra stateful sidecar and a data-retention surface.
  * **Telemetry is enabled by default** in 4.1; disabled explicitly in the PoC.
  * Output is Markdown with `<!-- page N of M -->` markers; **no per-element bbox** on the flash path.
  * Default output is truncated at 30 000 characters with a cursor; the adapter raises the limit.
  * Local flash models are PP-DocLayoutV2 + PP-OCRv6 (Apache-2.0); the AGPL-3.0 MinerU VLM weights
    are only used by the managed tiers.
* **Measured:** 0.85–1.7 s per document warm on GPU; ~880 MiB VRAM held by the daemon;
  OCR CER on the English scan 0.664, Chinese scan 0.451, 30-page native PDF 0.046.
* **Verdict:** keep as an **optional GPU engine for difficult scans/layouts**; do not make it the
  default, and never enable the remote tiers.

## 6. Apache Tika 4.2.0

* **What it is:** the JVM-based broad-coverage extractor.
* **Strengths:** widest format coverage of all candidates; 4–140 ms per document; correct GB18030
  decoding; useful backstop for formats no other engine handles.
* **Weaknesses observed**
  * **PDF:** plain text only — no bbox, no heading levels, no table structure, and
    **no page boundaries** (verified: 0 form feeds on a 3-page file). ParseDock records
    `no_coordinates` + `no_page_boundaries` for those.
  * **OOXML/HTML:** Tika 4.2 returns *markdown* (heading markers, pipe tables), which
    the adapter lifts into IR elements — so it does have DOCX structure, just no
    coordinates.
  * No OCR by default (scanned PDF text is empty or partial).
  * **Does not reject the zip bomb** used in the test corpus (returned HTTP 200 in 14 ms);
    ParseDock's own pre-flight validation is what stops it.
  * 1.37 GB image, JVM service to operate, `latest-full` is a moving tag (digest must be pinned).
* **Verdict:** fallback only (`mode=fast`, low routing priority), not a primary engine.

## 7. Candidate exit record

| Candidate | Exit rule triggered? | Evidence |
| --- | --- | --- |
| Xberg | no | installs, parses all target formats, MIT core; GPL helper lib needs a packaging decision |
| docling.rs | no | installs, best structure/quality on CPU |
| Docling / docling-serve | no (kept as oracle/optional service) | 7.2 GB env and 2 s/request overhead; quality equal to docling-rs |
| MinerU | **partially** — advanced tiers excluded | only `flash` is local; remote tiers upload documents; AGPL VLM weights |
| Apache Tika | no (kept as fallback) | text-only quality, no layout, no built-in resource bounds |

Nothing was silently skipped: every candidate above was installed, run on the corpus in
`benchmark/fixtures`, and has raw JSONL evidence.
