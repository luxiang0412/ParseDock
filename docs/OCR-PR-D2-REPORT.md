# PR-D2 report — GPU worker image: Vulkan prerequisites, and where it stops

> Status: **BLOCKED on this host**, with a reproducible root cause. The image
> change that removes one blocker is included; the change that would make MinerU
> use the GPU is **not** possible here and is not claimed.
>
> Image built for this investigation: `parsedock-worker-gpu:0.2.0-d2` (the
> production tag `0.1.0` and the running containers were not touched).

## 1. Why MinerU ran on the CPU

MinerU 4.1.2 has no CUDA execution provider: on Linux its device selection is
WebGPU (Dawn) → Vulkan. `mineru/model/runtime/onnx_device.py` only knows
`dml` (Windows) and `webgpu`. The daemon log said exactly that:

```
ONNX device selection failed; using CPU for this process:
    libvulkan.so.1: cannot open shared object file: No such file or directory
```

The image shipped no Vulkan loader and no ICD manifest, and the container toolkit
mounts the driver libraries but *not* `/usr/share/vulkan/icd.d/nvidia_icd.json`.

## 2. What the image now ships

| Change | Why |
| --- | --- |
| `libvulkan1` in the builder stage | the loader (`libvulkan.so.1`); carried into the final image by the existing `/usr/lib/x86_64-linux-gnu` copy |
| `libx11-6`, `libxext6`, `libxcb1` | `libGLX_nvidia.so.0` (the NVIDIA ICD) links against them; without them `dlopen` fails and the loader reports "Failed loading library associated with ICD JSON" |
| `/usr/share/vulkan/icd.d/nvidia_icd.json` | points at `libGLX_nvidia.so.0`, `api_version` 1.3.242 — the toolkit does not provide it |

Verified in `parsedock-worker-gpu:0.2.0-d2`: the loader and manifest are present,
`ldd libGLX_nvidia.so.0` has **no** unresolved dependency, and the old error is
gone. MinerU still runs on the CPU, for the reason in §3.

## 3. Where it stops (and what was ruled out)

With the loader and ICD in place the failure moves one layer down:

```
ERROR | DRIVER: loader_scanned_icd_add: Could not get 'vkCreateInstance'
                via 'vk_icdGetInstanceProcAddr' for ICD libGLX_nvidia.so.0
ERROR | DRIVER: vkCreateInstance: Found no drivers!
vkCreateInstance(apiVersion 1.3/1.2/1.1) -> -9  (VK_ERROR_INCOMPATIBLE_DRIVER)
```

A direct call to the ICD bypassing the loader shows the same thing:

| Call | Host | Container |
| --- | --- | --- |
| `dlopen("libGLX_nvidia.so.0")` | ok | ok |
| `vk_icdGetInstanceProcAddr(NULL, "vkCreateInstance")` | **non-NULL** | **NULL** |
| `vkCreateInstance(1.1/1.2/1.3)` | **0 (success)** | **-9** |
| `cuInit(0)` (CUDA) | 0 | **0** |

Everything the driver could ask for is present in the container, and was checked
one by one:

* identical library (`sha256` of `libGLX_nvidia.so.595.99.02` matches the host);
* all NVIDIA userspace libs mounted (glcore, eglcore, glvkspirv, glsi, allocator);
* `libGLX.so.0`, `libGLdispatch.so.0` present;
* `/dev/nvidia*`, `/dev/nvidia-modeset`, `/dev/nvidia-caps`, `/dev/dri/*` visible;
* `/proc/driver/nvidia/*` and `/sys/bus/pci/devices/0000:01:00.0` visible;
* `NVIDIA_DRIVER_CAPABILITIES=all|compute,utility,graphics` — no change;
* `--security-opt seccomp=unconfined apparmor=unconfined` — no change;
* `--gpus all`, CDI (`--device nvidia.com/gpu=all`), `-v /sys` — no change.

Conclusion: this is an **NVIDIA driver 595.99.02 / container-toolkit interaction**
(library initialises on the host, refuses inside the container), not something a
Dockerfile can fix. Installing Mesa's `lvp` (llvmpipe) would make Vulkan "work" in
software and would then be *misreported* as GPU execution by PR-D1's evidence —
that is explicitly not an option.

## 4. Consequence for PR-D1

Nothing regresses: `gpu_required` still fails closed (`gpu_unavailable`) and
`gpu_preferred` still records `cpu`, now with the loader and ICD in place so the
evidence line is `cpu_verified(mineru:daemon-log-window)` instead of an
unexplained unknown. `tests/integration/pr08_gpu.py` accepts exactly two
outcomes: proven GPU, or the documented refusal.

## 5. Options, ranked

1. **Use docling-serve with CUDA for GPU OCR** (already adapted in this repo).
   Torch CUDA in containers is a well-trodden path — the earlier Phase-1
   measurement on this same card showed 13.83 s → 7.52 s on a 30-page document at
   65 % utilisation. This is the only GPU route with evidence of working here.
2. **Re-test MinerU/Vulkan on a newer driver/toolkit** (or another host) using
   `benchmark/scripts/ocr_gpu_ab.py`; the image prerequisites are now in place, so
   the only unknown is the driver/container interaction.
3. **Ask upstream** (MinerU) about a CUDA EP path or a supported headless Vulkan
   configuration; until then, treat MinerU as a CPU engine in the GPU image.
4. Do **not** ship llvmpipe/software Vulkan.

## 6. What was not changed

Production image tag `parsedock-worker-gpu:0.1.0`, compose files, running
containers, public API, SQLite, ACK/TTL and the IR are untouched.
