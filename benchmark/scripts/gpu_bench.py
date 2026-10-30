#!/usr/bin/env python3
"""GPU verification: device visibility, VRAM delta, latency CPU vs GPU.

Measures with a sampler thread polling `nvidia-smi` every 200 ms, so the VRAM
numbers are observed deltas during the run rather than vendor claims.

Engines covered:
  * docling (Python CLI): --device cpu vs --device cuda
  * mineru 4.1 flash tier: local GPU pipeline (no CPU switch is exposed)

Writes benchmark/results/raw/gpu-<stamp>.jsonl and prints a markdown table.
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import statistics
import subprocess
import threading
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
FIX = ROOT / "benchmark" / "fixtures" / "generated"
RAW = ROOT / "benchmark" / "results" / "raw"
WORK = ROOT / "runtime" / "bench-work" / "gpu"
DOCLING = ROOT / "runtime" / "engines" / "venv-docling" / "bin" / "docling"
MINERU = ROOT / "runtime" / "engines" / "venv-mineru" / "bin" / "mineru"


def nvidia() -> dict:
    try:
        out = subprocess.run(
            ["nvidia-smi", "--query-gpu=memory.used,utilization.gpu,temperature.gpu",
             "--format=csv,noheader,nounits"],
            capture_output=True, text=True, timeout=5).stdout.strip().splitlines()
        mem, util, temp = [x.strip() for x in out[0].split(",")]
        return {"mem_mb": int(mem), "util": int(util), "temp": int(temp)}
    except Exception:
        return {"mem_mb": 0, "util": 0, "temp": 0}


class Sampler(threading.Thread):
    def __init__(self):
        super().__init__(daemon=True)
        self.stop_flag = threading.Event()
        self.peak_mem = 0
        self.peak_util = 0
        self.peak_temp = 0
        self.samples = 0
        self.base = nvidia()["mem_mb"]

    def run(self):
        while not self.stop_flag.is_set():
            s = nvidia()
            self.peak_mem = max(self.peak_mem, s["mem_mb"])
            self.peak_util = max(self.peak_util, s["util"])
            self.peak_temp = max(self.peak_temp, s["temp"])
            self.samples += 1
            time.sleep(0.2)

    def stop(self):
        self.stop_flag.set()
        self.join(timeout=5)

    def report(self) -> dict:
        return {
            "baseline_vram_mb": self.base,
            "peak_vram_mb": self.peak_mem,
            "vram_delta_mb": max(0, self.peak_mem - self.base),
            "peak_gpu_util": self.peak_util,
            "peak_temp_c": self.peak_temp,
            "samples": self.samples,
        }


@dataclass
class Case:
    engine: str
    device: str
    file: str
    cmd: list[str]
    outdir: Path = field(default_factory=lambda: WORK / "out")
    env: dict = field(default_factory=dict)


def run_case(c: Case, timeout: int = 1800) -> dict:
    if c.outdir.exists():
        shutil.rmtree(c.outdir, ignore_errors=True)
    c.outdir.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, **c.env}
    cmd = [x.replace("{outdir}", str(c.outdir)) for x in c.cmd]
    sampler = Sampler()
    sampler.start()
    t0 = time.perf_counter()
    try:
        p = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout, env=env)
        rc, err = p.returncode, p.stderr[-1500:]
    except subprocess.TimeoutExpired:
        rc, err = -1, "timeout"
    wall = time.perf_counter() - t0
    sampler.stop()
    produced = [str(p.relative_to(c.outdir)) for p in c.outdir.rglob("*") if p.is_file()]
    rec = {
        "ts": datetime.now(timezone.utc).isoformat(),
        "engine": c.engine, "device": c.device, "file": c.file,
        "cmd": " ".join(c.cmd), "rc": rc, "wall_ms": round(wall * 1000, 1),
        "outputs": produced[:10], "stderr_tail": err if rc != 0 else "",
    }
    rec.update(sampler.report())
    return rec


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--repeats", type=int, default=3)
    ap.add_argument("--only", default="")
    args = ap.parse_args()

    WORK.mkdir(parents=True, exist_ok=True)
    RAW.mkdir(parents=True, exist_ok=True)

    files = {
        "pdf_text_en_3p": FIX / "pdf_text_en_3p.pdf",
        "pdf_text_en_30p": FIX / "pdf_text_en_30p.pdf",
        "pdf_scanned_en": FIX / "pdf_scanned_en.pdf",
    }
    cases: list[Case] = []
    for key, path in files.items():
        for dev in ("cpu", "cuda"):
            cases.append(Case(
                engine="docling", device=dev, file=key,
                cmd=[str(DOCLING), "convert", str(path), "--to", "json",
                     "--output", "{outdir}", "--device", dev, "--num-threads", "8"],
            ))
        cases.append(Case(
            engine="mineru", device="gpu", file=key,
            cmd=[str(MINERU), "parse", str(path), "--tier", "flash", "--pages", "all",
                 "--force", "--wait", "900", "--format", "markdown", "--limit", "2000000",
                 "--output", "{outdir}/out.md"],
            env={},
        ))
    if args.only:
        pats = args.only.split(",")
        cases = [c for c in cases if any(p in f"{c.engine}/{c.device}/{c.file}" for p in pats)]

    records = []
    for c in cases:
        for r in range(args.repeats):
            rec = run_case(c)
            rec["repeat"] = r
            records.append(rec)
            print(f"{c.engine:8s} {c.device:5s} {c.file:16s} run{r} "
                  f"{rec['wall_ms']:8.0f}ms vram+{rec['vram_delta_mb']:6d}MB "
                  f"util{rec['peak_gpu_util']:3d}% rc={rec['rc']}")
            if rec["rc"] != 0:
                print("    " + rec["stderr_tail"].replace("\n", "\n    ")[:600])

    stamp = datetime.now().strftime("%Y%m%dT%H%M%S")
    out = RAW / f"gpu-{stamp}.jsonl"
    with open(out, "w") as f:
        for r in records:
            f.write(json.dumps(r) + "\n")

    print("\n| engine | device | file | wall (median) | VRAM delta | GPU util | runs |")
    print("| --- | --- | --- | --- | --- | --- | --- |")
    groups: dict[tuple, list[dict]] = {}
    for r in records:
        groups.setdefault((r["engine"], r["device"], r["file"]), []).append(r)
    for (eng, dev, file), rows in groups.items():
        ok = [r for r in rows if r["rc"] == 0]
        med = statistics.median(r["wall_ms"] for r in ok) if ok else float("nan")
        vram = max((r["vram_delta_mb"] for r in ok), default=0)
        util = max((r["peak_gpu_util"] for r in ok), default=0)
        print(f"| {eng} | {dev} | {file} | {med:.0f} ms | {vram} MB | {util}% | {len(ok)}/{len(rows)} |")
    print(f"\nraw: {out.relative_to(ROOT)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
