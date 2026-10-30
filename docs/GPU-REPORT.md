# GPU REPORT — Phase 0

> Host GPU: NVIDIA GeForce RTX 2080 Ti, 22 528 MiB, compute capability 7.5,
> driver 595.99.02, CUDA 13.2. Measurements by
> `benchmark/scripts/gpu_bench.py` (samples `nvidia-smi` every 200 ms and
> subtracts the idle baseline). Raw: `benchmark/results/raw/gpu-*.jsonl`.
>
> The GPU also drives the desktop session (~395 MiB idle, 1 866 MiB used at
> baseline), so VRAM deltas — not absolute values — are reported.

## 1. Verdict

| Question | Answer | Evidence |
| --- | --- | --- |
| Does the CPU-only path work with no GPU/CUDA at all? | **Yes** | T11 with xberg/docling-rs; container stack on CPU |
| Can containers see the GPU? | **Yes** | `docker run --device nvidia.com/gpu=all nvidia/cuda:13.2.0-base-ubuntu24.04 nvidia-smi` → RTX 2080 Ti, 22 528 MiB |
| Is GPU materially faster for Docling? | **Yes for multi-page documents, no for single pages** | 30 pages: 13.83 s CPU → 7.52 s CUDA (1.84×), +1 502 MB VRAM, 65 % util; 1 page: 6.26 s → 5.96 s (model load dominates) |
| Is MinerU usable locally on GPU? | **Only `flash` tier** | 0.88–1.57 s warm per document; daemon holds ≈ 880 MB VRAM |
| Does `gpu_required` silently fall back to CPU? | **No** | T12: `409 gpu_unavailable` when no GPU worker is registered |

## 2. Measured results

Median of 3 runs, warm server/process, wall clock includes process start or HTTP
round trip.

| Engine | Device | Document | Median wall | Peak VRAM delta | Peak GPU util |
| --- | --- | --- | --- | --- | --- |
| Docling 2.145.0 CLI | CPU | `pdf_text_en_3p` (3 p) | 6 261 ms | – | – |
| Docling 2.145.0 CLI | CUDA | `pdf_text_en_3p` | 5 955 ms | **+1 102 MB** | 20 % |
| Docling 2.145.0 CLI | CPU | `pdf_text_en_30p` (30 p) | 13 830 ms | – | – |
| Docling 2.145.0 CLI | CUDA | `pdf_text_en_30p` | **7 517 ms** | **+1 502 MB** | **65 %** |
| Docling 2.145.0 CLI | CPU | `pdf_scanned_en` (1 p, OCR) | 6 274 ms | – | – |
| Docling 2.145.0 CLI | CUDA | `pdf_scanned_en` (OCR) | 6 094 ms | +1 068 MB | 10 % |
| MinerU 4.1.2 `flash` | GPU | `pdf_text_en_3p` | 876 ms | 0 (daemon already resident) | 3 % |
| MinerU 4.1.2 `flash` | GPU | `pdf_text_en_30p` | 1 307 ms | 0 | 3 % |
| MinerU 4.1.2 `flash` | GPU | `pdf_scanned_en` | 1 572 ms | 0 | 9 % |

Interpretation:

* **Docling**: the CUDA gain appears only once the fixed cost (≈ 5.5 s of torch/ONNX
  model loading per CLI invocation) is amortised. For a *service* that keeps models
  resident (docling-serve), the GPU gain would show up at much smaller page counts.
  The CLI numbers above are a floor for GPU benefit, not a ceiling.
* **MinerU flash**: the local daemon keeps models on the GPU permanently
  (`nvidia-smi` went 1 866 → 2 749 MiB when the first parse ran, i.e. **≈ 880 MB
  resident**), so the per-request VRAM delta is 0 while GPU utilisation is low but
  non-zero. Latency is already sub-1.5 s per document on CPU-class workloads.
* **docling-rs 2.53.3** (the accurate CPU engine) has **no GPU path in the prebuilt
  CLI** — it reports `ONNX Runtime 1.31` and offers no device flag; the upstream
  project publishes CUDA-enabled Node bindings instead. A GPU variant would need a
  separate build/execution provider and is not part of Phase 0.
* **xberg 1.3.10** is CPU-only for its native/PDF path; its OCR backends are
  tesseract (CPU) or Paddle/VLM (model download, not enabled here).

## 3. What is verified vs. planned

| Item | Status |
| --- | --- |
| GPU visible to containers (CDI `nvidia.com/gpu=all`) | ✅ verified |
| Docling CUDA execution + VRAM measurement | ✅ verified |
| MinerU flash on GPU + resident VRAM | ✅ verified |
| `gpu_required` routing without GPU worker | ✅ verified (T12) |
| `deploy/compose.gpu.yaml` (MinerU worker image) | ⚠️ file provided, **image not built in Phase 0** — see §4 |
| GPU throughput at concurrency > 1 for docling-serve | ⚠️ not measured (docling-serve device switch not wired) |

## 4. Blocked / deferred, with the exact commands to finish

**GPU worker image.** `docker/Dockerfile.worker-gpu` builds on
`nvidia/cuda:13.2.0-runtime-ubuntu24.04` and installs `mineru==4.1.2`. It was not
built during Phase 0 because the resulting image is several GB and the PoC already
verified the same runtime natively. To finish it:

```bash
docker build -f docker/Dockerfile.worker-gpu -t parsedock-worker-gpu:0.1.0 .
docker compose -f deploy/compose.cpu.yaml -f deploy/compose.gpu.yaml --profile gpu up -d
docker compose -f deploy/compose.cpu.yaml exec worker-gpu nvidia-smi
curl -H "Authorization: Bearer $KEY" -F "file=@scan.pdf" -F "device=gpu_required" http://localhost:8080/v1/jobs
```

Acceptance to add once built: GPU worker registers `device_type=gpu`, a
`device=gpu_preferred` job routes to it (`routing.reason` shows the GPU engine), and
with the GPU worker stopped, `device=gpu_required` returns `409` (already covered by
T12 on CPU-only stacks).

**docling-serve GPU.** The installed 1.38.2 build exposes no documented
`device` request option in the tested configuration; the Python Docling CLI was used
for the CUDA measurement instead. To wire it: run docling-serve with a CUDA-enabled
torch and configure its accelerator options (P1 task, needs upstream docs review),
then repeat the `gpu_bench.py` matrix against `:15001`.

## 5. Operational notes

* GPU workers must be matched by capability, not by "there is a GPU box":
  registration carries `gpu_model`, `gpu_memory_bytes`, `device_type`, and the claim
  query excludes `gpu_required` jobs for CPU workers.
* VRAM planning: Docling CUDA peaks at ~1.5 GB for 30 pages at 1 worker; MinerU flash
  holds ~0.9 GB resident per daemon. A 22 GB card can host several workers, but
  model-load memory is per process — prefer one daemon with concurrency over N daemons.
* GPU time is not "free speed": the 30-page Docling run used 65 % GPU at ~7.5 s; the
  same work on CPU used 8 P-cores for 13.8 s. Choose per workload, and record the
  device in the result (`routing.actual_device`).
