# PR-D1 report — GPU execution evidence and the `gpu_required` contract

> Branch `feat/ocr-pr-d1-gpu-evidence`. Design: `docs/OCR-PR-D-DESIGN.md`
> (stage 1 implemented here; stage 2/3 remain). No image, container or public API
> was changed.

## 1. What was actually broken

`gpu_required` used to prove three things: an operator labelled a worker `gpu`,
the host has a GPU with free VRAM, and the engine adapter *declares* itself a GPU
engine (`mineru.Device()` returns `"gpu"` unconditionally). None of those says
anything about the task.

Measured on this host (RTX 2080 Ti, `parsedock-worker-gpu:0.1.0`): MinerU's own
daemon log says

```
ONNX session ready: kind=ocr_det, providers=['CPUExecutionProvider'], gpu=None
ONNX device selection failed; using CPU for this process: libvulkan.so.1: cannot open shared object file
```

so a `gpu_required` job completed successfully, with `routing.actual_device =
"gpu"`, while the work ran on the CPU. That is the contract violation this PR
fixes. (The missing Vulkan loader is a packaging bug and is **out of scope** here;
see §7.)

## 2. What was implemented

| Piece | Where |
| --- | --- |
| `Execution` value: `gpu_verified` / `cpu_verified` / `unknown`, with source and detail | `internal/engines/execution.go` |
| Per-request evidence callback (`Request.Report`), so the worker never touches an engine's `ExecResult` | `internal/engines/engine.go` |
| Per-engine evidence derivation | `xberg`, `docling-rs`, `docling-serve`, `mineru`, `native`, `tika` |
| Enforcement before publish (and after escalation) | `internal/worker/worker.go` |
| `routing.actual_device` derived from evidence, never from the declared device | `internal/worker/worker.go` |
| Registration payload states `gpu_inference_verified: false` + per-engine `gpu_evidence` | `internal/worker/worker.go` |
| Structured log line `execution evidence` (job, engine, requested device, kind, source) | `internal/worker/worker.go` |

Enforcement rule:

```go
// gpu_required is published only when this task provably ran on a GPU.
if err := gpuRequirementError(task.Device, engine.Name(), evidence); err != nil {
    return w.fail(ctx, task, jobs.ErrGPUUnavailable, err.Error(), false)
}
```

It reuses the existing stable code `gpu_unavailable` (409, non-retryable): **no
API change, no new error code**. The check runs before enrichment, escalation and
upload, and again after a retry, so no CPU-executed or unprovable attempt can be
published as a GPU result.

`gpu_preferred` keeps working: CPU fallback is allowed, but the result records
the device the evidence supports (`cpu`, or `unknown` when nothing could be
proven) instead of the engine's claim.

## 3. Where the evidence comes from, and how far it can be trusted

| Engine | Evidence | Trust |
| --- | --- | --- |
| `xberg` | adapter never requests an accelerator (`--acceleration` is not passed; the pinned build has no CUDA); the engine's own "compiled without CUDA support" message is honoured if it ever appears | proves **CPU only**. It can never prove GPU, by construction. |
| `docling-rs` | `DOCLING_RS_EP` unset/`cpu` ⇒ CPU (upstream default); a requested GPU provider plus the engine's fallback message (`built without that provider … using CPU`) ⇒ CPU; a request with no marker ⇒ **unknown** | proves **CPU**; GPU is reported as unknown because the engine prints no per-task device line |
| `mineru` | the daemon log is snapshotted before the task (byte offset) and read after it; only lines written **during the task** count (`providers=[…]`) | proves **GPU or CPU for this task** when a session line appears in the window. If models were already loaded (no line in the window), the daemon's full log may be used for one conclusion only: *every* session in it is CPU ⇒ CPU. A start-up GPU line is never accepted as proof for the task. |
| `docling-serve` | none | always **unknown** |
| `native`, `tika` | in-process Go / JVM extractor | proves **CPU** |

This is the distinction the review asked for: *GPU visible* (nvidia-smi,
registration), *provider initialised* (a start-up log line), and *this task used
the GPU* (a line written during the task, or an accepted proof). Only the third
one satisfies `gpu_required`.

## 4. Real acceptance (this host, no mocks)

`tests/integration/pr08_gpu.py` — coordinator from this tree, GPU worker image
`parsedock-worker-gpu:0.1.0` with **this tree's binary mounted read-only** (the
image itself is untouched, and the MinerU runtime is byte-identical):

```
PR08-01 PASS mineru inside the image → MinerU version: 4.1.2
PR08-02 PASS device_type=gpu free_vram=18830MiB model=NVIDIA GeForce RTX 2080 Ti
PR08-03 PASS gpu_required → failed 'gpu_unavailable' (expected fail-closed without GPU proof)
PR08-04 PASS gpu_preferred → completed engine=mineru actual_device='cpu'
              reason='…; exec=cpu_verified(mineru:daemon-log-startup)'
4/4 checks passed
```

Before PR-D1 the same run produced `PR08-03 PASS … completed … actual_device='gpu'`
while MinerU executed on the CPU. The mounting is deliberate: without it the old
binary inside the image would answer, and the test would pass on the
coordinator's behalf instead of testing the worker.

CPU-side regressions:

| Suite | Result |
| --- | --- |
| `go test ./...` | pass (15 new unit tests) |
| `pytest tests/unit` | 62 pass |
| `ci-local lifecycle` (PR-00…PR-12 runtime contracts, incl. backup drill) | 7/7, pass |
| `acceptance.py --profile real --engines native,xberg,docling-rs` | T01/T02/T11 pass |
| Playground E2E (desktop, real backend) | 10 passed, 1 skipped |

New unit tests: `TestGPURequirementMatrix` (7 cases: proven GPU passes; known CPU,
unprovable, missing-report all fail; gpu_preferred allowed on CPU/unknown),
`TestActualDeviceComesFromEvidence`, `TestRegistrationDoesNotClaimVerifiedGPU`,
`TestMineroExecution…` (task-window only, mixed providers ⇒ CPU),
`TestDaemonLogWindowIgnoresEarlierLines` (a start-up CUDA line is not this task's
evidence), `TestMineruStartupEvidenceOnlyProvesCPU`, `TestDoclingExecution`
(5 cases incl. silent fallback ⇒ CPU and no marker ⇒ unknown), `TestXbergExecution`.

## 5. Paths that still cannot be verified

1. **A real GPU execution.** No engine on this host can currently produce GPU
   evidence for a task: MinerU cannot initialise a GPU provider (missing
   `libvulkan.so.1`), docling-rs prints no device line, xberg has no CUDA build.
   So `gpu_verified` is exercised by unit tests only, and `gpu_required` jobs
   *must* fail here — which is the correct behaviour, not a gap in the fix.
2. **`docling-serve`** — an HTTP service that says nothing about per-request
   device. Unknown by construction; `gpu_required` will refuse it.
3. **A daemon that reloads models mid-task on a different provider** — MinerU
   would emit new session lines inside the window, and the rule then reports CPU
   if any line is CPU, so the failure mode is fail-closed.
4. **Evidence is per adapter statement, not OS-level attestation.** A patched
   engine that lies about its provider cannot be detected. The threat model is
   "misconfiguration and silent fallback", not adversarial engines.

## 6. Compatibility

| Surface | Change |
| --- | --- |
| Public API / error codes | none (`gpu_unavailable` reused) |
| `routing.actual_device` | values are now `gpu` \| `cpu` \| `unknown`. CPU deployments are unchanged (`cpu` as before, because those adapters already declared `cpu`). A GPU deployment whose engine cannot prove a GPU now reports `unknown` instead of a false `gpu`. |
| `routing.reason` | on **GPU workers only**, a `; exec=<kind>(<source>)` suffix is appended. CPU-only deployments keep byte-identical reason strings. |
| SQLite / migrations | untouched |
| ACK / TTL / IR schema | untouched |
| Container images | untouched (the test mounts the binary; production images are not rebuilt) |
| `gpu_required` behaviour | **intentionally stricter**: jobs that used to "succeed" on CPU now fail with `gpu_unavailable` where an engine cannot prove GPU use. This is the point of the PR. |

Operational consequence to announce: on a host where MinerU silently falls back,
`gpu_required` now returns `409 gpu_unavailable` with a message naming the engine
and the evidence source. That is a signal to fix the image (PR-D2), not to
disable the check.

## 7. PR-D2 suggestions (image fix, not done here)

1. Add the Vulkan loader and the NVIDIA ICD to `docker/Dockerfile.worker-gpu`
   (`libvulkan1`, and let `--gpus all` mount `nvidia_icd.json`; if not mounted,
   ship `/usr/share/vulkan/icd.d/nvidia_icd.json` pointing at the driver lib).
   Then re-run `pr08_gpu.py` unchanged: PR08-03 should flip to "completed with
   `actual_device='gpu'` and `exec=gpu_verified(mineru:daemon-log-window)`".
2. While there: pin the MinerU version and record the model cache hash.
3. Only after (1): consider the `require` stage of the design (coordinator
   filtering `gpu_required` to workers with proven engines) — unnecessary while
   the worker enforces the contract.
4. Add a scrape-time metric once evidence has a home in the database; today it
   lives in the IR (`actual_device`, `reason`) and in the worker log
   (`msg="execution evidence"`), which is documented in `docs/RUNBOOK.md`.
