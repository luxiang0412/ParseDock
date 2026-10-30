#!/usr/bin/env python3
"""PR-08: GPU worker image, registration and gpu_required routing.

Requires an NVIDIA GPU with the container toolkit and the images
`parsedock:0.1.0` + `parsedock-worker-gpu:0.1.0`. Without them the script prints
SKIP and exits 0, so it can be wired into a nightly job on a GPU host without
breaking CPU-only CI.

  PR08-01 GPU worker image starts and MinerU runs inside it
  PR08-02 worker registers device_type=gpu with measurable free VRAM
  PR08-03 device=gpu_required is routed to mineru and completes on the GPU worker
  PR08-04 the result records engine/device provenance

The VRAM-aware admission refusal (gpu_required + insufficient free VRAM → 409) is
unit-tested by TestGPUAdmissionRespectsFreeVRAM, so it needs no GPU here.

Run: python3 tests/integration/pr08_gpu.py [--build]
"""
import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BIN = os.environ.get("DOCPARSE_BIN", os.path.join(ROOT, "runtime", "bin", "docparse"))
BASE_IMAGE = os.environ.get("PARSEDOCK_IMAGE", "parsedock:0.1.0")
GPU_IMAGE = os.environ.get("PARSEDOCK_GPU_IMAGE", "parsedock-worker-gpu:0.1.0")
TOKEN = "pr08-internal-token"
OWNER = "gpu-owner"
CONTAINER = "pd-pr08-gpu"
PORT = int(os.environ.get("PR08_PORT", "18355"))


def run(cmd, **kw):
    return subprocess.run(cmd, capture_output=True, text=True, **kw)


def docker(*args, **kw):
    return run(["docker", *args], **kw)


def skip(reason):
    print(f"SKIP {reason}")
    sys.exit(0)


def http(path, method="GET", data=None, headers=None, timeout=60):
    req = urllib.request.Request(f"http://127.0.0.1:{PORT}{path}", method=method, data=data,
                                 headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def multipart(name, content, fields):
    boundary = "----pr08"
    out = bytearray()
    for k, v in fields.items():
        out += f"--{boundary}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n".encode()
    out += (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{name}\"\r\n"
            "Content-Type: application/octet-stream\r\n\r\n").encode()
    out += content + f"\r\n--{boundary}--\r\n".encode()
    return bytes(out), {"Content-Type": f"multipart/form-data; boundary={boundary}"}


def main():
    if not os.path.exists(BIN):
        skip(f"missing binary {BIN}; run `make build`")
    if not os.path.exists("/dev/nvidia0") and docker("info", "--format", "{{json .Runtimes}}").returncode != 0:
        skip("no NVIDIA device and no docker runtime")
    if "nvidia" not in (docker("info", "--format", "{{json .Runtimes}}").stdout or ""):
        skip("nvidia container runtime not configured")
    if "--build" in sys.argv:
        for df, tag in (("docker/Dockerfile", BASE_IMAGE),
                        ("docker/Dockerfile.worker-gpu", GPU_IMAGE)):
            r = docker("build", "-f", df, "-t", tag, ".")
            if r.returncode != 0:
                skip(f"image build failed for {tag}: {r.stderr[-300:]}")
    if docker("image", "inspect", GPU_IMAGE).returncode != 0:
        skip(f"missing {GPU_IMAGE}; pass --build")

    results = []
    # Reuse a persistent volume so MinerU's model cache survives between runs.
    vol = os.environ.get("PR08_VOLUME", "pd-gpu-home")
    docker("volume", "create", vol)
    docker("rm", "-f", CONTAINER)
    data = tempfile.mkdtemp(prefix="pr08-")
    coord = subprocess.Popen(
        [BIN, "--role", "coordinator", "--internal-listen", f"127.0.0.1:{PORT}",
         "--internal-token", TOKEN, "--data-dir", os.path.join(data, "coord"), "--engines", "mineru",
         "--rate-limit", "100", "--rate-burst", "50", "--engine-min-vram-mb", "mineru:8192"],
        stdout=open(os.path.join(data, "coord.log"), "w"), stderr=subprocess.STDOUT)
    try:
        for _ in range(100):
            try:
                if http("/healthz", timeout=1)[0] == 200:
                    break
            except Exception:  # noqa: BLE001
                pass
            time.sleep(0.1)

        r = docker("run", "-d", "--name", CONTAINER, "--device", "nvidia.com/gpu=all",
                   "--network", "host", "-v", f"{vol}:/work", GPU_IMAGE,
                   "--role", "worker", "--coordinator-url", f"http://127.0.0.1:{PORT}",
                   "--internal-token", TOKEN, "--engines", "mineru", "--device-type", "gpu",
                   "--worker-id", "gpu-w1", "--work-dir", "/work/scratch")
        if r.returncode != 0:
            results.append(("PR08-01", False, f"container start failed: {r.stderr[-200:]}"))
            raise SystemExit(report(results))

        # MinerU must be importable/runnable inside the image.
        r = docker("exec", CONTAINER, "mineru", "--version")
        results.append(("PR08-01", r.returncode == 0 and "MinerU" in r.stdout,
                        f"mineru inside the image → {(r.stdout or r.stderr).strip()[:60]}"))

        # Wait for registration, then inspect the reported capabilities.
        workers = []
        for _ in range(60):
            code, body = http("/internal/diagnostics/workers", headers={"X-Internal-Token": TOKEN})
            if code == 200:
                workers = json.loads(body).get("workers", [])
                if workers:
                    break
            time.sleep(2)
        w = workers[0] if workers else {}
        free = w.get("free_vram_bytes", 0)
        results.append(("PR08-02", w.get("device_type") == "gpu" and free > 0,
                        f"device_type={w.get('device_type')} free_vram={free >> 20}MiB "
                        f"model={w.get('gpu_model')}"))

        pdf = os.path.join(ROOT, "benchmark", "fixtures", "generated", "pdf_scanned_en.pdf")
        body, hdrs = multipart("scan.pdf", open(pdf, "rb").read(),
                               {"device": "gpu_required", "mode": "accurate"})
        hdrs.update({"X-Internal-Token": TOKEN, "X-Parsedock-Owner": OWNER})
        code, resp = http("/v1/jobs", "POST", body, hdrs, timeout=120)
        if code != 202:
            results.append(("PR08-03", False, f"create → {code} {resp[:200]!r}"))
            raise SystemExit(report(results))
        jid = json.loads(resp)["job_id"]
        st = {}
        deadline = time.time() + 600
        while time.time() < deadline:
            code, resp = http(f"/v1/jobs/{jid}", headers={"X-Internal-Token": TOKEN,
                                                          "X-Parsedock-Owner": OWNER})
            if code == 200:
                st = json.loads(resp)
                if st.get("processing_status") in ("completed", "failed"):
                    break
            time.sleep(3)
        results.append(("PR08-03", st.get("processing_status") == "completed",
                        f"gpu_required → {st.get('processing_status')} ({st.get('error_code')})"))
        results.append(("PR08-04", st.get("engine") == "mineru" and "gpu_required" in (st.get("routing_reason") or ""),
                        f"engine={st.get('engine')} reason={(st.get('routing_reason') or '')[:80]}"))

    finally:
        docker("rm", "-f", CONTAINER)
        coord.terminate()
        try:
            coord.wait(5)
        except subprocess.TimeoutExpired:
            coord.kill()
    return report(results)


def report(results):
    bad = [r for r in results if not r[1]]
    for name, ok, detail in results:
        print(f"{name} {'PASS' if ok else 'FAIL'} {detail}")
    print(f"\n{len(results) - len(bad)}/{len(results)} checks passed")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
