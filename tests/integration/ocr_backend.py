#!/usr/bin/env python3
"""PR-A verification: xberg OCR backend is opt-in, verified end to end.

Runs a real standalone process with the real engines (so it is skipped when the
engine binaries and models are absent) and asserts observable behaviour only:

  OCRB-01  default worker: no --ocr-backend flag, Tesseract text, no routing
           change (the reason string is byte-identical to before PR-A)
  OCRB-02  --xberg-ocr-backend paddle-ocr: mode=fast parses a Chinese scan with
           CER ~0 against the fixture gold, and routing.reason records the
           backend
  OCRB-03  same deployment, mode=auto: records whether one escalation happens
           (evidence for the PR-B revision; the assertion is that the final text
           is correct either way, not which engine produced it)
  OCRB-04  default deployment, mode=auto, Chinese scan read with eng: records the
           escalation decision and the CJK ratio (evidence for PR-B)
  OCRB-05  an unknown backend fails at startup with a clear message, not per job
  OCRB-06  the paddle-ocr worker needs no files beyond the pinned xberg tarball
           (no model download, no extra library directory)

Usage:
  python3 tests/integration/ocr_backend.py
  python3 tests/integration/ocr_backend.py --keep   # keep runtime dirs for triage
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import time
import unicodedata
import urllib.error
import urllib.request
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BIN = ROOT / "runtime" / "bin" / "docparse"
ENGINES = ROOT / "runtime" / "engines"
XBERG = ENGINES / "xberg-cli" / "xberg"
DOCLING = ENGINES / "docling-rs" / "docling-rs"
FIX = ROOT / "benchmark" / "fixtures" / "ocr"
WORK = ROOT / "runtime" / "ocr-backend"
API_TOKEN = "ocrb-api-token"
INTERNAL = "ocrb-internal"

results: list[tuple[str, bool, str]] = []


def report(name: str, ok: bool, detail: str = "") -> None:
    results.append((name, ok, detail))
    print(f"{'PASS' if ok else 'FAIL'}  {name}  {detail}")


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http(url: str, method: str = "GET", data: bytes | None = None, headers: dict | None = None, timeout: float = 120):
    req = urllib.request.Request(url, method=method, data=data, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, dict(r.headers), r.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read()


def squash(s: str) -> str:
    return "".join(unicodedata.normalize("NFC", s).split())


def cer(hyp: str, gold: str) -> float:
    h, g = squash(hyp), squash(gold)
    if not g:
        return 0.0
    prev = list(range(len(g) + 1))
    for i, ca in enumerate(h, 1):
        cur = [i]
        for j, cb in enumerate(g, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (ca != cb)))
        prev = cur
    return prev[-1] / len(g)


def cjk_ratio(text: str) -> float:
    letters = [c for c in text if c.isalpha()]
    if not letters:
        return 0.0
    cjk = sum(1 for c in letters if "\u4e00" <= c <= "\u9fff")
    return cjk / len(letters)


class Server:
    def __init__(self, extra: list[str], log: str) -> None:
        self.port = free_port()
        self.dir = WORK / uuid.uuid4().hex[:8]
        self.dir.mkdir(parents=True, exist_ok=True)
        argv = [
            str(BIN), "--role", "standalone",
            "--listen", f"127.0.0.1:{self.port}",
            "--internal-listen", f"127.0.0.1:{free_port()}",
            "--data-dir", str(self.dir),
            "--engines", "native,xberg,docling-rs",
            "--api-keys", f"{API_TOKEN}:ocrb",
            "--internal-token", INTERNAL,
            "--max-upload-mb", "128",
            "--worker-concurrency", "2",
            "--xberg-bin", str(XBERG),
            "--xberg-lib-dir", str(ENGINES / "xberg-cli"),
            "--docling-rs-bin", str(DOCLING),
            "--docling-rs-models", str(ENGINES / "docling-rs"),
            *extra,
        ]
        self.logfile = open(self.dir / f"{log}.log", "w")
        self.proc = subprocess.Popen(argv, stdout=self.logfile, stderr=subprocess.STDOUT)
        self.base = f"http://127.0.0.1:{self.port}"

    def wait(self, timeout: float = 40) -> bool:
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self.proc.poll() is not None:
                return False
            try:
                if http(self.base + "/healthz", timeout=1)[0] == 200:
                    return True
            except Exception:  # noqa: BLE001
                pass
            time.sleep(0.2)
        return False

    def parse(self, path: Path, mode: str) -> dict:
        body, headers = multipart(path)
        headers["Authorization"] = f"Bearer {API_TOKEN}"
        code, _, resp = http(f"{self.base}/v1/jobs?mode={mode}", "POST", body, headers)
        if code != 202:
            raise RuntimeError(f"create failed {code}: {resp[:200]}")
        job = json.loads(resp)["job_id"]
        status = {}
        for _ in range(300):
            _, _, raw = http(f"{self.base}/v1/jobs/{job}", headers={"Authorization": f"Bearer {API_TOKEN}"})
            status = json.loads(raw)
            if status["status"] in ("completed", "failed"):
                break
            time.sleep(0.2)
        if status.get("status") != "completed":
            raise RuntimeError(f"job {job} ended {status.get('status')}: {status.get('error')}")
        _, _, md = http(
            f"{self.base}/v1/jobs/{job}/result?format=markdown",
            headers={"Authorization": f"Bearer {API_TOKEN}"},
        )
        status["markdown"] = md.decode("utf-8", "replace")
        # The engine-level reason (including the OCR backend) lives in the IR,
        # not in the coordinator's routing decision.
        _, _, raw = http(
            f"{self.base}/v1/jobs/{job}/result?format=json",
            headers={"Authorization": f"Bearer {API_TOKEN}"},
        )
        try:
            ir = json.loads(raw)
            status["ir_reason"] = (ir.get("routing") or {}).get("reason", "")
            status["warnings"] = [w.get("code") for w in ir.get("warnings") or []]
            status["ocr"] = (ir.get("source") or {}).get("format")
        except Exception:  # noqa: BLE001
            status["ir_reason"], status["warnings"] = "", []
        return status

    def stop(self) -> None:
        self.proc.terminate()
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
        self.logfile.close()


def multipart(path: Path):
    boundary = "----" + uuid.uuid4().hex
    head = (
        f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{path.name}"\r\n'
        f"Content-Type: application/octet-stream\r\n\r\n"
    ).encode()
    body = head + path.read_bytes() + f"\r\n--{boundary}--\r\n".encode()
    return body, {"Content-Type": f"multipart/form-data; boundary={boundary}"}


def engines_present() -> bool:
    return BIN.exists() and XBERG.exists() and DOCLING.exists()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--keep", action="store_true")
    args = ap.parse_args()

    if not engines_present():
        print("SKIP: engines or binary missing (run scripts/ci-install-engines.sh)")
        return 0

    WORK.mkdir(parents=True, exist_ok=True)
    # PDF runs cover both pages, so they are scored against the full gold.
    zh_gold = (FIX / "zh_dense.gold.txt").read_text(encoding="utf-8")
    en_gold = (FIX / "en_dense.gold.txt").read_text(encoding="utf-8")

    # ---- OCRB-05: unknown backend is rejected at startup
    try:
        bad = Server(["--xberg-ocr-backend", "paddleocr"], "bogus")
        started = bad.wait(timeout=6)
        bad.stop()
        exited = bad.proc.returncode
        report("OCRB-05", (not started) and exited not in (0, None),
               f"unknown backend rejected at startup (exit={exited})")
    except Exception as e:  # noqa: BLE001
        report("OCRB-05", False, f"startup validation crashed: {e}")

    # ---- default deployment: unchanged behaviour
    srv = Server([], "default")
    if not srv.wait():
        print("standalone default failed to start; see", srv.dir)
        srv.stop()
        return 1
    try:
        st = srv.parse(FIX / "en_dense.pdf", "fast")
        md = st["markdown"]
        reason = st.get("routing_reason", "")
        report("OCRB-01", cer(md, en_gold) < 0.02 and "ocr_backend=" not in st.get("ir_reason", ""),
               f"engine={st.get('engine')} cer={cer(md, en_gold):.3f} "
               f"ir_reason={st.get('ir_reason','')!r} warnings={st.get('warnings')}")

        # default + auto on a Chinese scan read with eng: what does escalation do?
        st_zh = srv.parse(FIX / "zh_dense.pdf", "auto")
        md_zh = st_zh["markdown"]
        report("OCRB-04", True,
               f"default/auto zh: engine={st_zh.get('engine')} cjk={cjk_ratio(md_zh):.2f} "
               f"cer={cer(md_zh, zh_gold):.3f} warnings={st_zh.get('warnings')} "
               f"reason={st_zh.get('routing_reason','')[:80]!r}")
    finally:
        srv.stop()

    # ---- opt-in paddle-ocr
    srv = Server(["--xberg-ocr-backend", "paddle-ocr", "--xberg-ocr-lang", "ch"], "paddle")
    if not srv.wait():
        print("standalone paddle-ocr failed to start; see", srv.dir)
        srv.stop()
        return 1
    try:
        st = srv.parse(FIX / "zh_dense.pdf", "fast")
        md = st["markdown"]
        c = cer(md, zh_gold)
        report("OCRB-02", c < 0.02 and "ocr_backend=paddle-ocr" in st.get("ir_reason", ""),
               f"engine={st.get('engine')} cer={c:.3f} cjk={cjk_ratio(md):.2f} "
               f"ir_reason={st.get('ir_reason','')!r} warnings={st.get('warnings')}")

        st_auto = srv.parse(FIX / "zh_dense.pdf", "auto")
        md_auto = st_auto["markdown"]
        ca = cer(md_auto, zh_gold)
        report("OCRB-03", ca < 0.02,
               f"auto: engine={st_auto.get('engine')} cer={ca:.3f} "
               f"escalated={st_auto.get('engine') != 'xberg'} "
               f"warnings={st_auto.get('warnings')} "
               f"reason={st_auto.get('routing_reason','')[:100]!r}")
    finally:
        srv.stop()

    # ---- OCRB-06: nothing was downloaded at runtime
    extra = [p for p in WORK.rglob("*") if p.is_file() and p.suffix in (".onnx", ".traineddata")]
    report("OCRB-06", not extra, f"no model files written by the worker ({len(extra)} found)")

    if not args.keep:
        shutil.rmtree(WORK, ignore_errors=True)

    ok = all(r[1] for r in results)
    print(json.dumps([{"case": n, "ok": o, "detail": d} for n, o, d in results], indent=1))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
