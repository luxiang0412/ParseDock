# PR-D design — `gpu_required` must prove GPU inference (fail-closed)

> Status: **design + evidence only**. Production GPU routing is unchanged in this
> round on purpose.
>
> Evidence: `tests/integration/pr08_gpu.py` (existing), the MinerU daemon logs
> quoted below, `docs/OCR-GPU-BENCHMARK.md` §3, `docs/OCR-GPU-DECISIONS.md`.

## 1. The hole

Nothing in the request path documents *why* a job needed a GPU, and nothing
verifies that a GPU was used. Chain, with the code that implements each step:

| Step | Code | What it actually proves |
| --- | --- | --- |
| Worker declares itself GPU | `--device-type=gpu` → `worker.register` sends `device_type` (`internal/worker/worker.go:219`) | an **operator statement** |
| Worker reports free VRAM | `sysinfo.FreeVRAMMB` on every heartbeat (`worker.go:739`) | a GPU **exists** and has free memory — not who uses it |
| Coordinator routes `gpu_required` | `service.go:409` – `countGPUWorkers(engine, MinVRAMMB)` | a GPU worker is alive with room |
| Worker refuses CPU engines | `worker.go:378` – `engine.Device() == "cpu"` | the **adapter's static claim** (`mineru.go:50` returns `"gpu"` unconditionally when `DeviceStr` is empty) |
| Result reports the device | `Document.Routing.ActualDevice = m.Device()` | the same claim, copied into the IR |

So `gpu_required` proves: *an operator labelled a worker GPU, the worker has a GPU
with free memory, and the adapter claims to be a GPU engine.* It does **not**
prove that a single kernel ran on the card.

## 2. Evidence that the claim can be false

MinerU Flash, `parsedock-worker-gpu:0.1.0`, started through its own entrypoint
(the same path `pr08_gpu.py` uses):

```
nvidia-smi (300 ms sampling, whole run):  max 0 % utilisation, no VRAM delta
latency with MINERU_LMDEPLOY_DEVICE=cpu :  4 248 ms
latency with MINERU_LMDEPLOY_DEVICE=cuda:  4 290 ms      (statistically identical)
```

and its own daemon log says why:

```
... on ready: kind=ocr_det, providers=['CPUExecutionProvider'], gpu=None, ...
... on ready: kind=ocr_rec, providers=['CPUExecutionProvider'], gpu=None, ...
... on ready: kind=layout,  providers=['CPUExecutionProvider'], gpu=None, ...
[mineru.model.runtime.onnx_device] ONNX device selected:
    OnnxSelection(device='cpu', adapter=None, reason='selection_failed', ...)
... ONNX device selection failed; using CPU for this process:
    libvulkan.so.1: cannot open shared object file
```

MinerU 4.1.2 in this image is an **ONNX Runtime** pipeline (no `torch`), and its
GPU path goes through **Vulkan**. The image does not ship `libvulkan.so.1`, so the
selection fails and MinerU silently falls back to CPU — while the worker keeps
advertising `device_type=gpu` and the IR keeps reporting `actual_device=gpu`.

This is a **packaging bug, not a MinerU limitation**: the missing loader can be
added to `docker/Dockerfile.worker-gpu` (`libvulkan1` + the NVIDIA ICD json that
`--gpus all` mounts). Whether GPU inference then works is a separate, testable
question that this spike did not answer (installing packages inside a throwaway
container failed: the build network is not reachable from the daemon).

Second, independent hole: **docling-rs falls back silently**. On a CPU build,
`DOCLING_RS_EP=cuda` prints `this binary was built without that provider … using
CPU` to stderr and exits **0** with a valid result. Any worker that reads stdout
only cannot tell this apart from a real GPU run.

## 3. Fail-closed design

### 3.1 Positive evidence per engine

Every engine adapter gains a method:

```go
// GPUEvidence returns nil when the just-finished parse can be shown to have run
// on a GPU, or an error explaining which evidence is missing.
GPUEvidence(res *CLIResult) error
```

`CLIResult` already carries `Stderr` and `ExitCode`, so this needs no new
plumbing in `exec.go`.

| Engine | Evidence today | Rule |
| --- | --- | --- |
| `xberg` | fails loudly when a CUDA EP was requested but not compiled in (`… compiled without CUDA support; rebuild with the 'cuda' feature`, exit ≠ 0) | absent that error **and** an acceleration flag that is not `cpu` ⇒ proven-by-non-refusal; today our adapter never passes `--acceleration`, so the honest answer is **unverifiable** |
| `docling-rs` | stderr contains `built without that provider` / `using CPU` on fallback | if `EP` requested is `cuda`/`tensorrt`: refuse on that marker; without an EP flag, **unverifiable** |
| `mineru` | its daemon log names the provider (`providers=['CUDAExecutionProvider'], gpu=0`) | look for a **positive** marker (`CUDAExecutionProvider` / `gpu=0` / `device='cuda'`); `CPUExecutionProvider` or `gpu=None` ⇒ **fail** |
| `native`, `tika`, `docling-serve` | none | always unverifiable |

An engine that cannot produce positive evidence is `gpu_evidence=unverifiable`.

### 3.2 Enforcement point

In `worker.execute`, after `engine.Parse` and **before** publishing:

```go
if task.Device == jobs.DeviceGPURequired {
    if err := engine.GPUEvidence(res); err != nil {
        return w.fail(ctx, task, jobs.ErrGPUUnavailable, "gpu_required: "+err.Error(), false)
    }
}
```

* reuses the existing `gpu_unavailable` code and 409 mapping — **no new public
  error code**, no API change;
* non-retryable (`false`): retrying on the same worker cannot help;
* the result is never published, so `actual_device=gpu` is never written for an
  attempt that could not prove it.

### 3.3 Registration must not over-promise

Add `gpu_evidence: ["mineru"]` to the register payload (a capability list, not a
boolean), and have `countGPUWorkers` require the engine to be listed when the
deployment runs in `--gpu-evidence=require` (new flag, default `warn`).

Staged rollout, because this round must not change production routing:

| Stage | Behaviour | When |
| --- | --- | --- |
| **0 (this round)** | nothing enforced; evidence documented | now |
| **1** | `--gpu-evidence=warn` (default): run the check, emit
  `parsedock_gpu_evidence_failed_total` and a `gpu_unverified` IR warning, still
  publish the result | PR-D-1 |
| **2** | `--gpu-evidence=require`: `gpu_required` jobs that cannot prove it fail
  with `gpu_unavailable`; `gpu_preferred` still falls back to CPU | PR-D-2, after
  the image is fixed |
| **3** | coordinator filters `gpu_required` to workers advertising the engine |
| **D-3** | remove the warning once every GPU path is proven |

### 3.4 `actual_device` must not lie

`Document.Routing.ActualDevice` currently copies the adapter's claim. Under
`warn` mode it gains a second permitted value: `gpu_unverified` (registered in
`docs/DOCUMENT-IR.md`), which is *not* a schema change — the field is a string —
but it is an output change and therefore belongs to PR-D-1, not to a silent patch.

## 4. Tests for PR-D

| Test | Type | Asserts |
| --- | --- | --- |
| `TestMineruGPUEvidenceCPUProvider` | unit | a stderr/stdout sample with `providers=['CPUExecutionProvider'], gpu=None` ⇒ error |
| `TestMineruGPUEvidenceCUDAProvider` | unit | `providers=['CUDAExecutionProvider'], gpu=0` ⇒ nil |
| `TestDoclingRSFallbackIsRefused` | unit | `built without that provider … using CPU` ⇒ error; a clean run with `EP=cuda` ⇒ nil |
| `TestXbergCUDARefusalIsRefused` | unit | `compiled without CUDA support` in stderr ⇒ error |
| `TestGPURequiredUnprovenFails` | worker | `gpu_required` + unverifiable engine ⇒ attempt fails `gpu_unavailable`, no result published |
| `TestGPUPreferredStillFallsBack` | worker | `gpu_preferred` + unverifiable engine ⇒ completes on CPU |
| `TestGpuEvidenceWarnMode` | worker | `warn` mode publishes the result and adds `gpu_unverified` + metric |
| `PR08-04` (integration) | real GPU | MinerU job: the evidence marker is present **or** the job fails, never "completed + claimed GPU" |

`PR08-04` is the test that would have caught this: the existing suite asserts
"completes on the GPU worker", which CPU execution also satisfies.

## 5. What this does *not* decide

Whether to fix the image (`libvulkan1`), whether MinerU is worth keeping, and
whether `gpu_required` should keep `mineru` in its engine list at all. Those need
a rebuilt image and a 30-page document, both out of scope here.
