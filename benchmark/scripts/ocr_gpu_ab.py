#!/usr/bin/env python3
"""GPU A/B for OCR: docling-rs CUDA vs CPU, and what MinerU flash actually uses.

Two questions, two experiments, both on the same fixture and the same host:

1. **docling-rs** — does the CUDA build change latency, VRAM and *output* on a
   scanned page? Runs the official CUDA CLI image
   (``ghcr.io/docling-project/docling-rs-cuda``) three ways: ``DOCLING_RS_EP=cpu``,
   ``DOCLING_RS_EP=cuda`` and ``DOCLING_RS_EP=cpu`` with ``DOCLING_RS_FP32=1``.
   The fp32 arm exists because a GPU provider makes docling-rs prefer fp32
   graphs, which changes output with no GPU involved — without that arm a
   result difference looks like a CUDA bug when it is a precision choice.

2. **MinerU flash** — the GPU worker image starts a local MinerU daemon in its
   entrypoint, so this reproduces that exactly (uid 10001, ``HOME`` on the work
   volume) instead of calling ``mineru parse`` in a bare shell, which fails with
   "Local mineru server is not running".

GPU utilisation and VRAM come from sampling ``nvidia-smi`` every 300 ms for the
duration of the run. Provider initialisation is *not* evidence of GPU
inference: what matters is a VRAM delta above the idle baseline and non-zero
utilisation.

Usage:
    python3 benchmark/scripts/ocr_gpu_ab.py --json benchmark/results/ocr-gpu.json

Requires the two images (run ``docker pull`` first); without them the arms that
need them are skipped.
"""

from __future__ import annotations

import argparse
import json
import shutil
import statistics
import subprocess
import threading
import time
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
FIXTURES = REPO / "benchmark" / "fixtures" / "ocr"
DOCLING_IMAGE = "ghcr.io/docling-project/docling-rs-cuda:latest"
MINERU_IMAGE = "parsedock-worker-gpu:0.1.0"
MINERU_VOLUME = "parsedock-mineru-bench"
FIXTURE = "zh_dense.pdf"


class Sampler:
    """Sample nvidia-smi while a command runs."""

    def __init__(self, interval_ms: int = 300) -> None:
        self.interval_ms = interval_ms
        self.samples: list[tuple[int, int]] = []
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None

    def _loop(self) -> None:
        cmd = [
            "nvidia-smi", "--query-gpu=utilization.gpu,memory.used",
            "--format=csv,noheader,nounits", f"--loop-ms={self.interval_ms}",
        ]
        proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, text=True)
        assert proc.stdout is not None
        try:
            while not self._stop.is_set():
                line = proc.stdout.readline()
                if not line:
                    break
                parts = [p.strip() for p in line.split(",")]
                if len(parts) == 2 and parts[0].isdigit() and parts[1].isdigit():
                    self.samples.append((int(parts[0]), int(parts[1])))
        finally:
            proc.kill()

    def __enter__(self) -> "Sampler":
        self._thread = threading.Thread(target=self._loop, daemon=True)
        self._thread.start()
        time.sleep(0.4)
        return self

    def __exit__(self, *exc) -> None:
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=3)

    def summary(self) -> dict:
        if not self.samples:
            return {"samples": 0, "max_gpu_pct": None, "avg_gpu_pct": None, "max_vram_mib": None}
        util = [s[0] for s in self.samples]
        vram = [s[1] for s in self.samples]
        return {
            "samples": len(util),
            "max_gpu_pct": max(util),
            "avg_gpu_pct": round(sum(util) / len(util), 1),
            "max_vram_mib": max(vram),
            "min_vram_mib": min(vram),
        }


def image_present(image: str) -> bool:
    r = subprocess.run(["docker", "image", "inspect", image], capture_output=True, text=True)
    return r.returncode == 0


def run_docling(ep: str, fp32: bool, fixture: Path) -> dict:
    env = {"DOCLING_RS_EP": ep}
    if fp32:
        env["DOCLING_RS_FP32"] = "1"
    argv = ["docker", "run", "--rm", "--gpus", "all"]
    for k, v in env.items():
        argv += ["-e", f"{k}={v}"]
    argv += [
        "-v", f"{fixture.parent}:/data:ro",
        "--entrypoint", "docling-rs", DOCLING_IMAGE,
        "--to", "md", "--ocr-lang", "ch", f"/data/{fixture.name}",
    ]
    with Sampler() as s:
        t0 = time.time()
        proc = subprocess.run(argv, capture_output=True, text=True, timeout=1800)
        wall = (time.time() - t0) * 1000
    return {
        "arm": f"docling-rs ep={ep}{' fp32' if fp32 else ''}",
        "wall_ms": round(wall, 1),
        "rc": proc.returncode,
        "chars": len(proc.stdout),
        "sha256_of_output": __import__("hashlib").sha256(proc.stdout.encode()).hexdigest()[:16],
        "first_line": proc.stdout.splitlines()[0][:80] if proc.stdout else "",
        **s.summary(),
    }


MINERU_SCRIPT = r"""
set -eu
export HOME=/work/mineru-home HF_HOME=/work/mineru-home/hf XDG_CACHE_HOME=/work/mineru-home/.cache
mkdir -p "$HOME" /work/scratch /work/gpu-out
chown -R 10001:10001 /work 2>/dev/null || true
exec setpriv --reuid=10001 --regid=10001 --clear-groups sh -c '
  export HOME=/work/mineru-home HF_HOME=/work/mineru-home/hf XDG_CACHE_HOME=/work/mineru-home/.cache
  mineru telemetry disable >/dev/null 2>&1 || true
  mineru server start >/dev/null 2>&1 || true
  i=0; while [ "$i" -lt 120 ]; do mineru server status >/dev/null 2>&1 && break; i=$((i+1)); sleep 1; done
  t0=$(date +%s%N)
  mineru parse "/data/FIXTURE" --tier flash --force --wait 900 --format markdown \
      --limit 4000000 --output /work/gpu-out/flash.md --pages all >/work/gpu-out/parse.log 2>&1
  rc=$?
  t1=$(date +%s%N)
  echo "PARSE_RC=$rc ELAPSED_MS=$(( (t1-t0)/1000000 ))"
  sed -n "1,3p" /work/gpu-out/flash.md 2>/dev/null || true
'
"""


def run_mineru(fixture: Path) -> dict:
    subprocess.run(["docker", "volume", "create", MINERU_VOLUME], capture_output=True)
    script = MINERU_SCRIPT.replace("FIXTURE", fixture.name)
    argv = [
        "docker", "run", "--rm", "--gpus", "all",
        "-v", f"{fixture.parent}:/data:ro",
        "-v", f"{MINERU_VOLUME}:/work",
        "--entrypoint", "sh", MINERU_IMAGE, "-c", script,
    ]
    with Sampler() as s:
        t0 = time.time()
        proc = subprocess.run(argv, capture_output=True, text=True, timeout=1800)
        wall = (time.time() - t0) * 1000
    elapsed = None
    for line in proc.stdout.splitlines():
        if line.startswith("PARSE_RC="):
            parts = dict(p.split("=", 1) for p in line.split())
            elapsed = int(parts.get("ELAPSED_MS", 0))
    return {
        "arm": "mineru flash (GPU worker image, faithful entrypoint)",
        "wall_ms": round(wall, 1),
        "parse_ms": elapsed,
        "rc": proc.returncode,
        "output_head": "\n".join(proc.stdout.splitlines()[1:4]),
        **s.summary(),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--fixture", default=FIXTURE)
    ap.add_argument("--runs", type=int, default=2)
    ap.add_argument("--json", default="")
    args = ap.parse_args()
    fixture = FIXTURES / args.fixture
    if not fixture.exists():
        raise SystemExit(f"missing fixture {fixture} (run gen_ocr_fixtures.py)")

    rows: list[dict] = []
    if image_present(DOCLING_IMAGE):
        for ep, fp32 in (("cpu", False), ("cuda", False), ("cpu", True)):
            for i in range(args.runs):
                r = run_docling(ep, fp32, fixture)
                r["run"] = i + 1
                rows.append(r)
                print(f"  {r['arm']:<32} run={i + 1} wall={r['wall_ms']:>7.0f}ms "
                      f"gpu={r['max_gpu_pct']}% vram={r['max_vram_mib']}MiB out={r['sha256_of_output']}")
    else:
        print(f"  skip docling-rs arms: {DOCLING_IMAGE} not present")

    if image_present(MINERU_IMAGE):
        subprocess.run(["docker", "pull", "-q", MINERU_IMAGE], capture_output=True)
        subprocess.run(["docker", "volume", "create", MINERU_VOLUME], capture_output=True)
        for i in range(args.runs):
            r = run_mineru(fixture)
            r["run"] = i + 1
            rows.append(r)
            print(f"  {r['arm']:<32} run={i + 1} wall={r['wall_ms']:>7.0f}ms "
                  f"parse={r['parse_ms']}ms gpu={r['max_gpu_pct']}% vram={r['max_vram_mib']}MiB")
    else:
        print(f"  skip mineru arm: {MINERU_IMAGE} not present")

    if args.json and rows:
        Path(args.json).parent.mkdir(parents=True, exist_ok=True)
        Path(args.json).write_text(json.dumps(rows, indent=1) + "\n", encoding="utf-8")
        print(f"wrote {args.json}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
