# OCR benchmark — CPU backends and GPU A/B

> Host: i7-13700K (24 threads), 31 GiB, RTX 2080 Ti (22 528 MiB, compute 7.5),
> driver 595.99.02, CUDA 13.2, Docker 29.9.2, Go 1.24.4, Python 3.12.3.
> Date: 2026-10-31.
>
> Raw data: `benchmark/results/ocr-cpu.json`, `benchmark/results/ocr-cpu.md`,
> `benchmark/results/ocr-gpu.json`. Reproduce with
> `benchmark/scripts/ocr_bench.py` and `benchmark/scripts/ocr_gpu_ab.py`.
>
> Fixtures are **generated locally** (`benchmark/scripts/gen_ocr_fixtures.py`,
> seed 20261031): two A4 pages, 60 lines each, 300 dpi, image-only PDF (no text
> layer), gold text = exactly what was drawn. No document from `test_data/` was
> used, and nothing left the host.

## 1. Method

| Aspect | Choice |
| --- | --- |
| Input | `ocr/{en,zh}_dense.pdf` (2 pages, 60 lines), rendered from 300 dpi rasters |
| Gold | `*.gold.txt` (both pages) for PDF runs; `*.p1.gold.txt` (page 1) for image runs |
| CER | Levenshtein over characters, all whitespace removed, NFC, **no** case folding |
| digit CER | same distance over digits only — amounts, dates, invoice numbers |
| WER | whitespace tokens, punctuation stripped; English fixtures only |
| Latency | wall clock for one whole document **including process start and model load**, 3 runs (CPU) / 2 runs (GPU), p50 and p95 |
| Peak RSS | `/usr/bin/time -f %M` (whole process tree). RapidOCR measured separately: 558 MiB (small), 1 002 MiB (medium) |
| GPU | `nvidia-smi --loop-ms=300` sampling; idle baseline 3 149–3 152 MiB (desktop session) |

Everything runs exactly as the ParseDock adapters run it: no per-engine
pre-processing, no page splitting, no cropping. A backend that is slow because it
loads 1.4 GB of models per document is reported as slow — that is what a cold
worker would see.

## 2. CPU results

| Engine | Fixture | OK | p50 ms | p95 ms | RSS MiB | CER | digit CER | WER |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `xberg` tesseract `eng` | en_dense | yes | **387** | 390 | 225 | **0.000** | 0.000 | 0.000 |
| `xberg` tesseract `chi_sim` | en_dense | yes | 388 | 396 | 224 | 0.033 | 0.000 | 0.076 |
| `xberg` paddle-ocr `en` | en_dense | yes | 1 326 | 1 355 | 841 | **0.000** | 0.000 | 0.000 |
| `xberg` paddle-ocr `ch` | en_dense | yes | 1 359 | 1 367 | 893 | **0.000** | 0.000 | 0.000 |
| `xberg` paddle-ocr `ch` + `--acceleration cuda` | en_dense | **NO** | 522 | 543 | 225 | — | — | — |
| `xberg` tesseract `eng` + `--acceleration cuda` | en_dense | yes | 410 | 411 | 225 | 0.000 | 0.000 | 0.000 |
| `docling-rs` ppocr `en` | en_dense | yes | 1 540 | 1 557 | 1 462 | **0.000** | 0.000 | 0.000 |
| `docling-rs` ppocr `en`, `--ocr-mode full_page` | en_dense | yes | 1 552 | 1 578 | 1 461 | 0.000 | 0.000 | 0.000 |
| `docling-rs` ppocr `ch` | en_dense | yes | 1 577 | 1 594 | 1 462 | 0.000 | 0.000 | 0.000 |
| `docling-rs` ppocr `ch`, `full_page` | en_dense | yes | 1 512 | 1 512 | 1 462 | 0.000 | 0.000 | 0.000 |
| `docling-rs` `DOCLING_RS_EP=cuda` (CPU build) | en_dense | yes | 1 512 | 1 560 | 1 462 | 0.000 | 0.000 | 0.000 |
| `tesseract` CLI `eng` (PNG) | en_dense | yes | 641 | 645 | **120** | 0.000 | 0.000 | 0.000 |
| `tesseract` CLI `chi_sim` (PNG) | en_dense | yes | 920 | 927 | 150 | 0.003 | 0.000 | 0.041 |
| `rapidocr` PP-OCRv6 small | en_dense | yes | 1 809 | 1 810 | 558 | 0.000 | 0.000 | 0.000 |
| `rapidocr` PP-OCRv6 medium | en_dense | yes | **18 000** | 18 160 | 1 002 | 0.000 | 0.000 | 0.000 |
| `xberg` tesseract `eng` | zh_dense | yes | 422 | 423 | 225 | **0.453** | 0.102 | — |
| `xberg` tesseract `chi_sim` | zh_dense | yes | 406 | 418 | 224 | 0.075 | 0.000 | — |
| `xberg` paddle-ocr `en` | zh_dense | yes | 1 222 | 1 252 | 762 | **0.000** | 0.000 | — |
| `xberg` paddle-ocr `ch` | zh_dense | yes | 1 252 | 1 274 | 765 | **0.000** | 0.000 | — |
| `xberg` paddle-ocr `ch` + cuda | zh_dense | **NO** | 544 | 550 | 225 | — | — | — |
| `xberg` tesseract `eng` + cuda | zh_dense | yes | 420 | 423 | 225 | 0.453 | 0.102 | — |
| `docling-rs` ppocr `en` | zh_dense | yes | 1 708 | 1 714 | 1 408 | **0.002** | 0.000 | — |
| `docling-rs` ppocr `en`, `full_page` | zh_dense | yes | 1 700 | 1 743 | 1 407 | 0.002 | 0.000 | — |
| `docling-rs` ppocr `ch` | zh_dense | yes | 1 648 | 1 657 | 1 407 | **0.002** | 0.000 | — |
| `docling-rs` ppocr `ch`, `full_page` | zh_dense | yes | 1 670 | 1 708 | 1 407 | 0.002 | 0.000 | — |
| `docling-rs` `DOCLING_RS_EP=cuda` (CPU build) | zh_dense | yes | 1 662 | 1 699 | 1 408 | 0.002 | 0.000 | — |
| `tesseract` CLI `eng` (PNG) | zh_dense | yes | 638 | 642 | 123 | 0.432 | 0.111 | — |
| `tesseract` CLI `chi_sim` (PNG) | zh_dense | yes | 830 | 836 | 152 | 0.062 | 0.000 | — |
| `rapidocr` PP-OCRv6 small | zh_dense | yes | 1 739 | 1 777 | 558 | 0.004 | 0.000 | — |
| `rapidocr` PP-OCRv6 medium | zh_dense | yes | 16 754 | 16 788 | 1 002 | 0.000 | 0.000 | — |

### 2.1 What the numbers say

1. **Chinese**: PP-OCRv6 small is the only engine at CER 0.000 (every engine in
   the xberg, RapidOCR and docling-rs families runs this model family; docling-rs
   is 0.002 because it joins lines differently). Tesseract with the right
   `chi_sim` data is 6–7 % CER — mostly inserted spaces between characters —
   and with `eng` it is unusable (45 %).
2. **English**: tesseract wins on cost (387 ms, 225 MiB) and is exact. PP-OCRv6
   is also exact but 3.4× slower and 3.7× the memory.
3. **Language independence of PP-OCRv6 `ch`**: it reads English at CER 0.000 too,
   so a single backend can serve a mixed corpus. That is the single most useful
   finding for routing: it removes the "wrong tessdata language" failure mode.
4. **Model tier**: medium buys nothing on clean 300 dpi scans (CER identical) and
   costs 13× the latency. Small/mobile is the right default.
5. **`full_page` OCR mode** in docling-rs is neutral on our fixtures (1 512 vs
   1 540 ms; 0.002 both). Keep `auto`.
6. **`DOCLING_RS_EP=cuda` on the CPU build**: exit 0, same latency, same output —
   it warns on stderr and falls back. **A wrapper cannot tell this apart from a
   real GPU run**; only a check of the stderr text can.
7. **`xberg --acceleration cuda`**: fails loudly on the onion-model path
   (rc ≠ 0) and is silently ignored on the Tesseract path (rc 0). Both behaviours
   matter: the first is safe, the second is a trap.
8. **Startup cost dominates small documents**: docling-rs spends ~1.4 s of the
   1.65 s loading models; a warm daemon would change the ranking for
   many-small-file workloads. Our worker pays this per document today.

## 3. GPU A/B

Same fixture (`zh_dense.pdf`, 2 pages), same image, only the execution provider
changes. Output hashes are over the Markdown produced.

| Arm | Wall (run 1 / 2) | max GPU | avg GPU | max VRAM | Output sha256 |
| --- | --- | --- | --- | --- | --- |
| `docling-rs` CUDA image, `EP=cpu` | 2 809 / 2 444 ms | 0 % | 0 % | 3 149 MiB (baseline) | `656526b1…` |
| `docling-rs` CUDA image, `EP=cpu`, `FP32=1` | 2 718 / 2 960 ms | 0–2 % | ≈0 % | 3 149 MiB | `db993ca1…` |
| `docling-rs` CUDA image, `EP=cuda` | **42 269 / 42 921 ms** | **8 %** | **≈1 %** | **3 939–4 225 MiB** | `db993ca1…` |
| MinerU flash (`parsedock-worker-gpu:0.1.0`, faithful entrypoint) | 6 435 / 6 806 ms (parse 4 243 / 4 274 ms) | **0 %** | 0 % | no delta | correct Markdown |

Image versions: `docling-rs 2.55.4 (chunking, mcp, cuda)` in
`ghcr.io/docling-project/docling-rs-cuda:latest` (4.95 GB); the CPU binary in this
repository is `2.53.3` without the `cuda` feature.

### 3.1 Reading the GPU numbers

* **The CUDA provider initialises and is 15–18× slower.** 4.2 GB of VRAM is
  held (so the EP is real), utilisation never exceeds 8 %, and the latency is
  42 s against 2.5 s on CPU. The only consistent explanation is per-node CPU
  fallback on a compute-7.5 device — CUDA 13 / cuDNN 9 kernels targeting newer
  architectures. This is exactly the case the spike rule warns about: *provider
  initialised ≠ inference on the GPU*.
* **CUDA did not change the answer by itself.** `EP=cuda` output is
  **byte-identical** to `EP=cpu, FP32=1`. The difference from the default CPU arm
  comes from int8 → fp32 graph selection, which upstream documents as a
  consequence of running on a GPU provider. So adopting CUDA is also an output
  change, to be gated on the golden set — independent of speed.
* **MinerU flash did not use the GPU** on this host: zero utilisation, no VRAM
  delta, and identical latency with `MINERU_LMDEPLOY_DEVICE=cpu` and `=cuda`
  (4 248 vs 4 290 ms). The image has no `torch` at all. Either the flash tier in
  this build is CPU-only, or the daemon needs a device setting we have not found;
  either way, the "MinerU = GPU path" claim in `docs/GPU-REPORT.md` is **not
  reproducible today** and is marked unverified.
* The earlier Python-Docling measurement in `docs/GPU-REPORT.md`
  (13.83 s → 7.52 s on 30 pages, 65 % utilisation) shows GPU acceleration *can*
  work on this card with a different runtime. The failure here is specific to the
  Rust/ONNX-Runtime CUDA build, not to the GPU.

## 4. Capacity implications

Assumptions: 4 parse slots per host (`--worker-concurrency 4`), scans only, 2-page
documents, CPU only.

| Backend | Per document | Documents/s (4 slots) |
| --- | --- | --- |
| tesseract `eng` | 0.39 s | ≈ 10.3 |
| tesseract `chi_sim` | 0.41 s | ≈ 9.8 |
| xberg paddle-ocr (PP-OCRv6 small) | 1.25 s | ≈ 3.2 |
| docling-rs ppocr | 1.65 s | ≈ 2.4 |

`docs/SLO.md` targets 2.5 jobs/s sustained. PP-OCRv6 keeps the fast path above
that target; docling-rs alone would not, which is why it stays the *escalation*
engine rather than the default.

## 5. Caveats

* Gold is synthetic (one font family per language, no skew, no stamps, no
  handwriting, no fax artefacts, no tables). Rankings are trustworthy; absolute
  CERs are optimistic.
* Latency includes process start and model load — a warm daemon (docling-serve,
  MinerU) would show different absolute numbers, not different rankings.
* p95 over 3 runs is effectively the max; treat it as an upper bound, not a
  distribution.
* RapidOCR RSS comes from a separate measurement (its own process), the numbers
  in the table are from that run.
* One GPU, one driver, one image build. Nothing here generalises to Ampere/Ada or
  to the GPU images' next release.
