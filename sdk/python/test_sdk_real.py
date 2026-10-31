#!/usr/bin/env python3
"""Python SDK against the **real** engines (native + xberg), not the mock.

The mock engine proves the protocol; this proves the SDK can parse what the real
parsers actually return: the Document IR with its real element/table shapes, the
OCR warning vocabulary (`ocr_used`, `ocr_quality_unknown`), and a real Markdown
rendering.

Skipped (exit 0, with SKIP) when the engine binaries are not installed, so the
PR gate can run it unconditionally and the nightly job gets the real coverage.

Usage: python sdk/python/test_sdk_real.py
"""
from __future__ import annotations

import json
import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "sdk" / "python"))

from parsedock_sdk import ParseDockClient, ParseDockError  # noqa: E402

BIN = Path(os.environ.get("DOCPARSE_BIN") or (ROOT / "runtime" / "bin" / "docparse"))
ENGINES = ROOT / "runtime" / "engines"
XBERG = ENGINES / "xberg-cli" / "xberg"
PDF = ROOT / "benchmark" / "fixtures" / "generated" / "pdf_text_en_3p.pdf"
SCAN = ROOT / "benchmark" / "fixtures" / "ocr" / "zh_dense.pdf"
TOKEN = "sdk-real-token-01"
PORT, INT_PORT = 18188, 18189

failures: list[str] = []


def check(name: str, cond: bool, detail: str = "") -> None:
    if cond:
        print(f"[ok] {name}{(' — ' + detail) if detail else ''}")
    else:
        failures.append(f"{name}{(' — ' + detail) if detail else ''}")


def main() -> int:
    if not BIN.exists():
        raise SystemExit("build the binary first: make build")
    if not XBERG.exists():
        print("SKIP: xberg binary not installed (scripts/ci-install-engines.sh)")
        return 0
    for p in (PORT, INT_PORT):
        with socket.socket() as s:
            s.settimeout(0.3)
            if s.connect_ex(("127.0.0.1", p)) == 0:
                raise SystemExit(f"port {p} busy")

    data = Path(tempfile.mkdtemp(prefix="sdk-real-"))
    proc = subprocess.Popen(
        [str(BIN), "--role", "standalone",
         "--listen", f"127.0.0.1:{PORT}", "--internal-listen", f"127.0.0.1:{INT_PORT}",
         "--data-dir", str(data), "--work-dir", str(data / "work"),
         "--engines", "native,xberg", "--internal-token", "itest",
         "--api-keys", f"{TOKEN}:sdk-real",
         "--result-ttl", "180s", "--cleanup-interval", "1s", "--worker-lease", "15s",
         "--xberg-bin", str(XBERG), "--xberg-lib-dir", str(ENGINES / "xberg-cli")],
        env={**os.environ}, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        start_new_session=True)
    base = f"http://127.0.0.1:{PORT}"
    try:
        for _ in range(120):
            try:
                with urllib.request.urlopen(base + "/readyz", timeout=1):
                    break
            except Exception:  # noqa: BLE001
                time.sleep(0.2)
        else:
            raise SystemExit("stack did not become ready")

        with ParseDockClient(base, token=TOKEN) as c:
            # --- a born-digital PDF through the real parser
            job = c.create_job(PDF, mode="fast")
            final = c.wait(job["job_id"], timeout=120)
            check("real pdf completed", final["status"] == "completed",
                  f"engine={final.get('engine')} status={final['status']}")
            check("real pdf engine", final.get("engine") == "xberg", str(final.get("engine")))

            md, version = c.download(job["job_id"], fmt="markdown")
            ir_bytes, ir_version = c.download(job["job_id"], fmt="json")
            check("markdown not empty", len(md) > 200, f"{len(md)} bytes")
            check("result versions agree", version == ir_version == final.get("result_version"))

            ir = json.loads(ir_bytes)
            check("IR schema", ir.get("schema_version") == "doc-ir/0.1", str(ir.get("schema_version")))
            check("IR has elements", bool(ir.get("elements")), f"{len(ir.get('elements') or [])} elements")
            check("IR engine recorded", (ir.get("parser_engine") or "") == "xberg",
                  str(ir.get("parser_engine")))
            device = (ir.get("routing") or {}).get("actual_device")
            check("actual_device is a known value", device in ("cpu", "gpu", "unknown"), str(device))
            codes = [w.get("code") for w in ir.get("warnings") or []]
            check("warning codes are strings", all(isinstance(x, str) and x for x in codes), str(codes))
            # tables, when present, must be parseable by the SDK's consumers
            for t in ir.get("tables") or []:
                check("table grid", t.get("rows", 0) > 0 and t.get("cols", 0) > 0
                      and len(t.get("cells") or []) >= 1, f"{t.get('rows')}x{t.get('cols')}")

            # ACK the real result and prove the release contract still holds
            rel = c.ack(job["job_id"], final["result_version"])
            check("ack accepted", rel.get("retention_status") in ("release_pending", "released"),
                  str(rel.get("retention_status")))
            try:
                c.download(job["job_id"], fmt="markdown")
                failures.append("download after ACK did not return 410")
            except ParseDockError as e:
                check("download after ack is gone", e.status == 410, f"{e.status} {e.code}")

            # --- a scanned PDF: the SDK must survive the OCR warning vocabulary
            # xberg's OCR backend is the system tesseract; without it the scan
            # cannot be parsed, and that is an environment gap, not an SDK defect.
            if not shutil.which("tesseract"):
                print("[skip] scanned pdf: tesseract not installed")
                scan = None
            else:
                scan = c.create_job(SCAN, mode="fast")
            if scan is not None:
                sfinal = c.wait(scan["job_id"], timeout=180)
                if sfinal["status"] != "completed":
                    check("scanned pdf completed", False, str(sfinal.get("error_code")))
                else:
                    _, _ = c.download(scan["job_id"], fmt="markdown")
                    sir = json.loads(c.download(scan["job_id"], fmt="json")[0])
                    scodes = [w.get("code") for w in sir.get("warnings") or []]
                    check("scan completed via xberg", sfinal.get("engine") == "xberg", str(sfinal.get("engine")))
                    check("scan carries ocr_used", "ocr_used" in scodes, str(scodes))
                    check("scan text is non-empty",
                          sum(len(e.get("text") or "") for e in sir.get("elements") or []) > 100,
                          f"{len(sir.get('elements') or [])} elements")
    finally:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
            proc.wait(10)
        except Exception:  # noqa: BLE001
            proc.kill()
        shutil.rmtree(data, ignore_errors=True)

    if failures:
        for f in failures:
            print(f"[FAIL] {f}")
        return 1
    print("\nSDK real-engine: all checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
