# OCR / GPU decisions

> Spike 2026-10-31. Evidence for every verdict is in
> `docs/OCR-BACKEND-RESEARCH.md` (capabilities) and
> `docs/OCR-GPU-BENCHMARK.md` (measurements); raw data in
> `benchmark/results/ocr-cpu.{json,md}` and `benchmark/results/ocr-gpu.json`.
>
> Scope of this document: what we will do, what we will not do, and which
> decisions are still unsupported by evidence. No production code changes are
> included in the spike.

## 1. Verdicts

| # | Question | Verdict |
| --- | --- | --- |
| 1 | CPU Chinese OCR recommendation | **GO — xberg `paddle-ocr` (PP-OCRv6 small)**; `docling-rs` ppocr as the accurate path; tesseract only for Latin |
| 2 | GPU OCR recommendation | **NO-GO on current hardware** (RTX 2080 Ti, compute 7.5) / **CONDITIONAL** elsewhere |
| 3 | Add docling-rs CUDA to the production worker | **CONDITIONAL NO** — blocked on a faster GPU *and* a golden re-approval (CUDA switches int8 → fp32 and changes output) |
| 4 | Integrate an xberg GPU backend | **NO-GO** — no CUDA-enabled upstream build; would require us to build and ship our own CLI |
| 5 | Standalone PaddleOCR worker | **NO-GO** — xberg already embeds PP-OCRv6 small at CER 0.000 |
| 6 | Keep MinerU Flash | **CONDITIONAL** — keep the image and profile, stop claiming GPU acceleration until re-measured |
| 7 | Minimal API/config changes | **GO** for the backend/engine passthrough flags and the `ocr_quality_unknown` warning; **DEFER** password support and IR OCR geometry |
| 8 | Image size, dependencies, licence, security | **Acceptable** for the CPU path; **+4.95 GB** for a CUDA image; L1 unchanged |
| 9 | PR split | A–F in `docs/OCR-ROUTING-PROPOSAL.md` §5 |
| 10 | Judgements still lacking a real Gold Set | listed in §4 |

## 2. Why each verdict

### 2.1 CPU Chinese OCR: xberg `paddle-ocr` (GO)

Two-page dense Chinese scan, CPU, 3 runs, gold = generated text:

| Engine | p50 | CER | digit CER | peak RSS |
| --- | --- | --- | --- | --- |
| xberg `paddle-ocr ch` (PP-OCRv6 small) | 1 252 ms | **0.000** | 0.000 | 765 MiB |
| xberg `paddle-ocr en` | 1 222 ms | **0.000** | 0.000 | 762 MiB |
| docling-rs ppocr `ch` | 1 648 ms | 0.002 | 0.000 | 1 407 MiB |
| RapidOCR v6 small (standalone) | 1 739 ms | 0.004 | 0.000 | 558 MiB |
| tesseract `chi_sim` (via xberg) | 406 ms | 0.075 | **0.000** | 224 MiB |
| tesseract `eng` (wrong language) | 422 ms | 0.453 | 0.102 | 225 MiB |
| RapidOCR v6 medium | 16 754 ms | 0.000 | 0.000 | 1 002 MiB |

* PP-OCRv6 small reads **both** Chinese and English at CER 0.000 — the language
  trap that dominates tesseract (0.075 vs 0.453) disappears.
* It costs ~3× tesseract per scan page (1.25 s vs 0.41 s) and ~3.4× its RAM.
  At 4 parse slots that is still ~3.2 scans/s, comfortably above the 2.5 jobs/s
  target in `docs/SLO.md`.
* Medium tier is 13× slower for **no** CER gain → not worth it on CPU.
* Tesseract stays valuable for two things: Latin-only scans (0.000 at 387 ms) and
  the digit stream (CER 0.000 on digits even when the characters are wrong) — but
  digits alone do not make a document readable.

### 2.2 GPU OCR on the current host: NO-GO

| Arm | Wall (2 pages) | GPU util (max / avg) | VRAM | Output |
| --- | --- | --- | --- | --- |
| docling-rs CUDA image, `DOCLING_RS_EP=cpu` | ≈ 2.3 s | 0 % | baseline | full text, 1 797 bytes |
| docling-rs CUDA image, `DOCLING_RS_EP=cpu`, `DOCLING_RS_FP32=1` | ≈ 3.2 s | 0 % | baseline | full text, 1 826 bytes |
| docling-rs CUDA image, `DOCLING_RS_EP=cuda` | **≈ 42 s** | **≤ 8 % / ≈ 1 %** | **4.2 GB held** | byte-identical to the fp32 CPU arm |
| MinerU flash (GPU worker image, faithful entrypoint) | ≈ 4.3 s warm | **0 %** | **no delta** | correct Markdown |

Reading this honestly:

* The CUDA provider **initialises** (4.2 GB resident) and is **18× slower** with
  ~1 % utilisation: per-node CPU fallback on a compute-7.5 card is the only
  explanation consistent with the numbers. The spike rule applies directly —
  provider init is not GPU inference.
* CUDA did **not** corrupt output: it matched `DOCLING_RS_FP32=1` byte for byte.
  The output change comes from **precision selection**, not from the device.
* MinerU flash never touched the GPU in this image: no VRAM delta, no utilisation,
  and `MINERU_LMDEPLOY_DEVICE=cuda|cpu` produced the same 4.2 s. The PR-08 claim
  of ~880 MB VRAM and 876 ms–1.57 s is therefore **unverified** until the worker
  path is measured again on a host where the daemon actually binds CUDA.

### 2.3 docling-rs CUDA in the worker: CONDITIONAL NO

Blockers, in order:

1. No GPU here where CUDA wins; the verdict must be re-taken on Ampere+ (the
   GPU-REPORT measurement of Python Docling showed 1.84× on 30 pages, so the
   *idea* is not dead — this Rust build on this card is).
2. CUDA flips int8 → fp32, which changes output bytes. Our golden gate would have
   to be re-approved per engine, and `result_version` would silently differ
   between a GPU and a CPU worker — an operational trap for a fleet with mixed
   hardware.
3. The image is +4.95 GB and pulls the CUDA 13 + cuDNN 9 runtime, on top of L1.

### 2.4 xberg GPU backend: NO-GO

* The released CLI is compiled without CUDA and fails loudly when asked
  (`… compiled without CUDA support; rebuild with the 'cuda' feature`).
* There is no CUDA release asset; a production integration would mean building
  xberg ourselves and shipping another 100 MB+ binary, with the same L1 question.
* Even then the gain is limited to the ONNX models (PP-OCR det/rec, layout);
  extraction stays CPU.

### 2.5 Standalone PaddleOCR worker: NO-GO

xberg 1.3.10 already runs PP-OCRv6 small in-process at CER 0.000. A separate
worker would add a second engine protocol, ~0.5–1 GB RSS per replica and a
model-cache problem, to gain nothing measurable.

### 2.6 MinerU flash: CONDITIONAL

* Keep it as the `gpu` profile's accurate option — but the docs and `docs/GPU-REPORT.md`
  must stop asserting GPU acceleration until it is re-measured.
* If it turns out to be CPU-only in this image, its value drops to "a
  layout+babeldoc pipeline with more dependencies and a daemon", and the accurate
  path (`docling-rs`) covers the same need at 1/5 the image size.

## 3. What this spike does *not* change

* No routing defaults, no API surface, no IR schema, no worker protocol, no
  Coordinator/SQLite change, no image rebuild.
* The CPU-only deployment remains the supported deployment; nothing here requires
  CUDA to build or run.

## 4. Judgements that still lack a real Gold Set

| Claim | Why it is still weak | What would settle it |
| --- | --- | --- |
| "PP-OCRv6 is better for Chinese" | gold is generated text rendered at 300 dpi with one font family — no skew, no stamps, no handwriting, no fax noise | 30–50 real scanned pages from the audit corpus, manually transcribed, never leaving the host |
| "CER 0.000 is production quality" | synthetic scans have no layout complexity (no tables, columns, stamps) | a gold set that includes tables and multi-column scans |
| "table quality" | the fixtures contain **no tables**; TEDS/APTED not measured | scanned-table fixtures + a TEDS implementation (still a PR-11 gap) |
| "CUDA is slower on this GPU" | one card, one fixture, one build (2.55.4) | same arms on Ampere/Ada + a 30-page document |
| "MinerU runs on CPU here" | inferred from utilisation/VRAM/latency equality, not from a log line | instrument the daemon (`MINERU_*` device env) or read its verbose log in the worker integration test |
| "English on PP-OCRv6 `en` is fine" | CER 0.000 on our fixture, but the earlier 6-line fixture produced `Supler. Nwn Lic` | denser English scans, mixed fonts and sizes |

Until those exist, the numbers in this spike are **relative rankings on clean
synthetic scans**, not accuracy guarantees for the audit corpus.
