# OCR backends and GPU acceleration — source-level research

> Spike date: 2026-10-31. Host: i7-13700K (24 threads), 31 GiB RAM, RTX 2080 Ti
> (22 528 MiB, compute 7.5), driver 595.99.02, CUDA 13.2, Docker 29.9.2.
>
> Every claim below is labelled with how it was obtained:
> **B** = measured on a binary in this repository, **U** = read from upstream
> documentation/tags, **X** = not verified (do not rely on it).
>
> No material from `test_data/` was used. All fixtures are generated locally by
> `benchmark/scripts/gen_ocr_fixtures.py` (fictional content, OFL fonts).

## 1. What is installed, versus what upstream ships

| Component | In this repository | Latest upstream | Drift |
| --- | --- | --- | --- |
| `xberg` CLI | **1.3.10**, `xberg-cli-x86_64-unknown-linux-gnu.tar.gz` | 1.3.10 (released 2026-10-13) | none |
| `docling-rs` | **2.53.3**, ONNX Runtime 1.31, statically linked, **CPU build** | 2.55.4 (released 2026-10-30) | 2 minor versions |
| docling-rs models | `models-v1` (`images.lock.json`) | same download script | n/a |
| MinerU | **4.1.2** inside `parsedock-worker-gpu:0.1.0` | 4.1.2 | none |
| Tesseract | **5.5.3**, `tessdata` with `eng` + `chi_sim` | 5.5.x | none |
| RapidOCR (bench only) | **3.10.0** in `/tmp/ocrbench/venv` (not shipped) | 3.10.0 | none |

Evidence: `xberg --version` (B), `docling-rs --version` → `2.53.3 (chunking, mcp); ONNX Runtime 1.31` (B),
`gh api repos/{xberg-io/xberg,docling-project/docling.rs}/releases/latest` (U).

**We do not upgrade in this spike.** Upgrading docling-rs changes model graphs and
therefore parse output; that is a benchmark-gated change of its own.

## 2. xberg 1.3.10

### 2.1 OCR backends (B)

`xberg doctor` registers eight OCR backends:

| Backend | State on this host (B) | Notes |
| --- | --- | --- |
| `tesseract` | **pass** — tesseract 5.5.3, tessdata `eng` + `chi_sim` | CPU only, not an ONNX model |
| `paddle-ocr` | **runs** — `model_key=v6:small tier=mobile backend=Ort` | `doctor` reports "4 present, 33 missing" manifests because it only checks the *whole* optional model set; the chinese/english v6:small graphs are present |
| `sceptre` | skip — no probe | unverified |
| `vlm` | warn — needs `--vlm-model` + `--vlm-api-key` | remote model endpoint, out of scope (data egress) |
| `candle-trocr`, `candle-paddleocr-vl`, `candle-glm-ocr`, `candle-deepseek-ocr` | skip — no probe | model weights not shipped |

`doctor` has **no accelerator probe at all**: it never reports whether a backend
will run on a GPU. That absence is itself a finding — it is why a CUDA request
must be tested by execution, not by inspection.

### 2.2 Language codes (B)

* Tesseract: ISO 639-3 (`eng`, `chi_sim`, …). `chi_sim` is what the tessdata
  directory contains for Chinese.
* PaddleOCR: short codes (`en`, `ch`, `french`, `korean`). `--ocr-language en`
  selects `family="english"`, `ch` selects `family="chinese"` (confirmed in the
  init log line).
* A wrong pairing does not fail: it produces garbage with `quality score: 1.00`.
  Measured: English scan read with `chi_sim` → CER 0.033; Chinese scan read with
  `eng` → CER 0.453.

### 2.3 Concurrency knobs (B)

`--max-concurrent` (batch parallelism), `--max-threads` (total thread budget),
`--max-concurrent-ocr` (OCR sessions). With 24 CPUs and no `max_threads` set,
xberg caps itself at **8** threads and logs a warning; our worker does not pass
these flags, so it inherits the cap.

### 2.4 GPU (`--acceleration`) — the important part (B)

`--acceleration auto|cpu|coreml|cuda|tensorrt` selects the **ONNX Runtime
execution provider** for model inference only. It does *not* accelerate
extraction, and it is irrelevant to Tesseract (which is not an ONNX model).

Measured behaviour of the release build:

```
$ xberg extract --ocr true --ocr-backend paddle-ocr --ocr-language ch \
      --acceleration cuda fixture.pdf
WARN ... OCR backend failed for page page=1 error=OCR error: Failed to initialize
PaddleOCR models for chinese (v6:small) on the ort backend: Ort error: CUDA
execution provider requested but this build was compiled without CUDA support;
rebuild with the `cuda` feature.
exit status 1          ← fails loudly, no silent CPU fallback
```

```
$ xberg extract --ocr true --ocr-backend tesseract --ocr-language eng \
      --acceleration cuda fixture.pdf
→ exit 0, identical output                ← provider flag is simply unused
```

So:

1. The released xberg CLI is **compiled without CUDA**; enabling it requires
   building from source with the `cuda` feature (U: the error message states it,
   and the release asset list contains one Linux tarball, no CUDA variant).
2. On the model path, a CUDA request **fails loudly** — good, unlike docling-rs.
3. On the Tesseract path, `--acceleration cuda` is silently a no-op. A caller
   that "enables CUDA" and gets exit 0 has proven nothing.

As of 1.3.10 there is **no prebuilt CUDA-enabled xberg release asset** (U: 63
release assets inspected, all `xberg-cli-*` / language bindings).

## 3. docling-rs

### 3.1 Version and build (B)

`docling-rs 2.53.3 (chunking, mcp); ONNX Runtime 1.31` — the feature list has no
`cuda`, the 71 MB binary is statically linked against ONNX Runtime and ships **no**
`libonnxruntime_providers_cuda.so`. It is a CPU build.

Upstream (U, `docs/MODELS.md` §GPU execution providers):

```bash
cargo build --release -p docling-cli --features cuda      # NVIDIA CUDA (Linux/Windows)
cargo build --release -p docling-cli --features tensorrt  # usually with cuda
DOCLING_RS_EP=cuda docling-rs input.pdf                   # this provider or fail loudly
```

Prebuilt CUDA artefacts exist (U, verified with `docker manifest inspect`):
`ghcr.io/docling-project/docling-rs-cuda` (**4.95 GB**, `linux/amd64`, reports
`docling-rs 2.55.4 (chunking, mcp, cuda)`) and
`ghcr.io/docling-project/docling-rs-serve-cuda` (4.94 GB, `docling-serve` +
`libonnxruntime_providers_cuda.so`). Wheels/npm: `docling-rs-cuda`,
`docling.rs-cuda`.

### 3.2 Execution-provider semantics — measured, and different from the docs (B)

| Situation | Documented (U) | Measured (B) |
| --- | --- | --- |
| CPU build + `DOCLING_RS_EP=cuda` | "this provider or fail loudly" | **warning + silent CPU fallback, exit 0** (`… this binary was built without that provider — rebuild with --features cuda; using CPU`) |
| CUDA build + `DOCLING_RS_EP=cuda` | runs on GPU | runs on GPU (4.2 GB VRAM held) |
| CUDA build + `DOCLING_RS_EP=cpu` | CPU | CPU |

The warning goes to **stderr only**; exit code stays 0 and output is valid. In a
worker that captures stdout and ignores stderr, this is indistinguishable from a
successful GPU run. Conclusion for ParseDock: **we cannot rely on the engine to
enforce "GPU required"** — the scheduler must.

### 3.3 Precision switching changes output (B)

Upstream: "the int8 graphs are the default; `DOCLING_RS_FP32=1` (or a GPU execution
provider) prefers the fp32 ones". Measured on `zh_dense.pdf`: CPU+int8 output =
1 797 bytes, CPU+fp32 = 1 826 bytes, CUDA = 1 826 bytes **byte-identical to
CPU+fp32**. So switching EP changes the *result*, not just the speed — any GPU
adoption must be treated as a benchmark-gated output change (our `result_version`
is content-addressed, so old and new results are distinguishable, but the golden
set must be re-approved).

### 3.4 OCR flags (B, from `--help`)

| Flag | Values | Our adapter |
| --- | --- | --- |
| `--ocr-engine` | `ppocr` (default) \| `tesseract` | not passed → ppocr (RapidOCR/PP-OCR ONNX) |
| `--ocr-lang` | `en` \| `ch`, BCP-47 (`en-US`, `zh`, `zh-Hans`), `iso:` prefix | passed, mapped from `--xberg-ocr-lang` (`chi_sim`/`zh` → `ch`, else `en`) |

### 3.5 Language codes are normalised by xberg itself (B, corrected)

An earlier note in this document assumed the two backends needed different
spellings. They do not: xberg 1.3.10 normalises ISO 639-3 and the short codes
into the same recognition family, so ParseDock can pass `--xberg-ocr-lang`
through unchanged.

| Passed | tesseract | paddle-ocr family |
| --- | --- | --- |
| `eng`, `en` | `eng` | `english` |
| `chi_sim`, `zh`, `ch` | `chi_sim` | `chinese` |
| `fra` | `fra` (tessdata) | `latin` |

Measured with `--ocr-backend paddle-ocr --ocr-language <code>` and reading the
`Initializing PaddleOCR engine family=…` log line. `eng` is a **valid** English
code for paddle-ocr; it is not a silent fallback to Chinese.
| `--ocr-mode` | `auto` \| `full_page` \| `layout_regions` | not passed (auto) |
| `--no-ocr`, `--text-layer-only[=auto]` | – | `--no-ocr` when OCR is off; `--text-layer-only` for `--docling-rs-text` |
| `--ocr-scale`, `--ocr-textline-orientation`, `--picture-ocr-scale` | – | not passed |
| `--password` | PDF/Office password | **not exposed** — 8 corpus files fail as `encrypted_file` although both engines could decrypt them |

## 4. MinerU 4.1.2 (`parsedock-worker-gpu:0.1.0`)

* The CLI (`mineru parse … --tier flash`) requires a **local daemon**;
  `docker/gpu-entrypoint.sh` starts it (`mineru server start`) before exec'ing the
  worker. Calling `mineru parse` in a bare shell fails with
  `Error: Local mineru server is not running. Run 'mineru server start'.` (B)
* Model cache: `MINERU_HOME`/`HF_HOME`/`XDG_CACHE_HOME` all point at
  `/work/mineru-home`, a named volume. A cold cache costs ~25 s on the fixture
  used here (B); warm runs are ~4.3 s for 2 pages (B).
* The image has **no `torch`** (`ModuleNotFoundError`, B) and
  `nvidia-smi` shows **0 % utilisation and no VRAM delta** during a warm
  `flash` parse (B, 300 ms sampling). `MINERU_LMDEPLOY_DEVICE=cuda` and `=cpu`
  produced the same 4.2 s warm latency (B). In other words: on this image the
  flash tier behaved as a CPU pipeline, and the pre-existing `docs/history/GPU-REPORT.md`
  claim ("daemon holds ≈ 880 MB VRAM", 876 ms–1.57 s) **could not be reproduced**
  and is now **[X] unverified** until the worker path is re-measured end to end.

## 5. How ParseDock drives all of this today

| Concern | Where | Current behaviour |
| --- | --- | --- |
| Engine choice | `internal/engines/capabilities.go`, `internal/coordinator/service.go` | `mode=fast` → xberg, `accurate` → docling-rs, text formats → native; `device=gpu_required` filters to GPU engines and needs a live GPU worker with free VRAM ≥ `MinVRAMMB` |
| Scan escalation | `escalate.go` | `mode=auto` + CJK ratio ≥ 0.15 + `ocr_low_confidence` → one re-run on the accurate engine |
| GPU admission | claim-time VRAM check | per-card free VRAM via `CUDA_VISIBLE_DEVICES`; `-1` unknown; GPU concurrency capped at 1 |
| Engine flags we pass | `internal/engines/{xberg,docling,mineru}.go` | xberg: `--ocr`, `--ocr-backend`(field exists, **no CLI flag**), `--ocr-language`. docling-rs: `--ocr-lang`, `--no-ocr`, `--text-layer-only`, `--no-table-former`. mineru: `parse --tier flash` |
| OCR language config | `--xberg-ocr-lang` (default `eng`) | a single global value for the whole worker; wrong value degrades silently |

Nothing in this list needs to change for a spike; the seams are already narrow.

## 6. OCR elements and geometry — what xberg actually returns (B)

`xberg extract --format json` on a scanned page, both backends:

| Field | tesseract | paddle-ocr | Usable for IR? |
| --- | --- | --- | --- |
| `result.metadata.ocr_used` | `true` | `true` | yes |
| `result.metadata.format.scanned_confidence` | `0.85` | `0.85` | yes (page-level) |
| `result.metadata.format.scanned_pages` | `[1, 2]` | `[1, 2]` | yes |
| `result.metadata.additional.ocr_processed_image_{width,height}` | 2 481 × 3 507 | 2 481 × 3 507 | **required** to interpret geometry |
| `result.pages[].ocr_confidence.score` | `0.85` | **`null`** | only tesseract |
| `result.pages[].ocr_confidence.word_count` | `205` | **`0`** | only tesseract |
| `result.pages[].hierarchy.blocks[].bbox` | present, **2 blocks** for a 30-line page | present, **1 block spanning the page** | coarse |
| per-word / per-line text + bbox | **absent** | **absent** | **no** |
| per-element detection/recognition confidence | **absent** | **absent** | **no** |

Coordinate evidence: `result.metadata.pages.pages[].dimensions` is 595.44 × 841.68
(PDF points) while the block bbox for the same page is
`left 255, top 907, right 982, bottom 3228` — i.e. **raster pixels**
(2481 × 3507), not points. The scale factor is
`ocr_processed_image_width / page_width_pt` ≈ 4.17.

Document IR already has the vocabulary for this: `SourceLocation.BBox` with
`Coord: "pdf_user_space_v1" | "image_px_v1"`, plus `PageWidthPt/PageHeightPt`,
`Element.Confidence`, and the warnings `ocr_low_confidence` /
`ocr_quality_unknown` (`internal/documentir/ir.go`). What is missing is a backend
that fills them per OCR element.

### 6.1 Table geometry: measured with a ruled-table fixture (B)

`benchmark/fixtures/ocr/zh_table.pdf` is a two-page image-only PDF with one ruled
table drawn at a known position (left 0.9 in, top 1.2 in, 4 columns × 1.6 in,
5 rows × 0.32 in → in points ≈ x 65…526, y 86…201; in 300 dpi pixels
≈ x 270…2 190, y 360…840).

| Input | `--layout` | Tables | bbox | Unit |
| --- | --- | --- | --- | --- |
| born-digital PDF (`pdf_text_en_3p`) | n/a | 1 | x 114.8…480.5, y 459.9…564.9 (page 595 × 842) | **PDF points** |
| scanned table (this fixture) | off (our adapter) | **0** | – | nothing emitted |
| scanned table | `--layout true` | 4 (over-segmented) | x 267.1…2 192.8, y 356.1…842.6 | **raster pixels** |

The scanned bbox matches the drawn table in pixel space to within ~3 px, and its
`x1` (2 192) is 3.7× the page width in points — so it is pixels, not points.
(`y1 = 842.6` only looks like the page height in points by coincidence: the
table's bottom edge is at 840 px.)

Consequences:

* `internal/engines/xberg.go` labels every xberg table bbox
  `pdf_user_space_v1`. That is correct for born-digital PDFs and **wrong** for
  OCR'd pages.
* The mislabel is currently **latent**, not active: the adapter does not pass
  `--layout`, and without it xberg reports **no tables at all** on scanned pages,
  so no scanned geometry is published today.
* Before `--layout` is ever exposed: normalise with
  `ocr_processed_image_{width,height}` ÷ page size and emit `image_px_v1`, or
  withhold the field. Emitting points-space geometry scaled from pixels without
  a fixture like the one above is exactly the fabricated precision this spike is
  supposed to prevent.

Two hazards, both real today:

1. The `pdf_user_space_v1` label above (latent for tables; already demonstrated
   for blocks on OCR'd pages, where `top` reaches 3 228 on an 842-point page).
2. `word_count = 0` from paddle-ocr means our `ocr_low_confidence` warning can
   only ever be produced by the tesseract backend, which is exactly the backend
   that is worse at Chinese. The warning currently says nothing about quality for
   the PP-OCR path.

Recommendation (matches the spike rule "only suggest mapping when data exists"):
**do not extend the IR for OCR elements yet.** Map nothing that is `null`, and
treat `--format json` block bboxes as `image_px_v1` until a fixture with a
scanned *table* proves otherwise.

## 7. Reproduction

```bash
python3 benchmark/scripts/gen_ocr_fixtures.py            # deterministic fixtures + gold
python3 benchmark/scripts/ocr_bench.py --runs 3 \
    --json benchmark/results/ocr-cpu.json --md benchmark/results/ocr-cpu.md
python3 benchmark/scripts/ocr_gpu_ab.py --runs 2 \
    --json benchmark/results/ocr-gpu.json
docker run --rm --gpus all --entrypoint docling-rs \
    ghcr.io/docling-project/docling-rs-cuda:latest --version   # shows the "cuda" feature
```

Fixture provenance: `benchmark/fixtures/ocr/MANIFEST.json` (sha256, sizes, DPI,
line counts); fonts are `NotoSansCJK` (OFL) / `DejaVuSans` (Bitstream Vera licence),
both already required by `gen_fixtures.py`.
