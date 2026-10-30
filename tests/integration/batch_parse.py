#!/usr/bin/env python3
"""Batch-parse a directory through a running ParseDock instance.

Built for real corpora (PR-11) and for ad-hoc evaluation of a customer folder.
It is **redacting by default**: filenames are replaced by a short hash and no
document text is stored, so the JSON report can be shared without leaking
content. Use `--no-redact` only for a private local debug run.

    python tests/integration/batch_parse.py --dir test_data \
        --api http://127.0.0.1:18090 --out runtime/testdata-run/report.json

Per file it records: status, engine, client latency, page count, character
count, table count, warning codes, error code and an output hash. It never
records the parsed text.

Supported extensions come from the server allow-list; everything else is
counted as skipped (zip archives, legacy .doc/.xls, lock files, ...).
"""
from __future__ import annotations

import argparse
import hashlib
import json
import mimetypes
import statistics
import sys
import threading
import time
import http.client
import urllib.error
import urllib.request
import uuid
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

# Mirrors internal/storage/sniff.go. The legacy OLE2 container is shared by Word, Excel and
# PowerPoint, so the extension decides; PR-04 taught the service to accept these, and the
# harness has to send them or it silently skips the very files under test.
ACCEPTED_EXT = {".pdf", ".docx", ".xlsx", ".xlsm", ".pptx", ".csv", ".tsv",
                ".txt", ".md", ".markdown", ".png", ".jpg", ".jpeg", ".webp",
                ".doc", ".dot", ".xls", ".xlt", ".ppt", ".pot"}


@dataclass
class Stats:
    lock: threading.Lock = field(default_factory=threading.Lock)
    records: list = field(default_factory=list)
    skipped: Counter = field(default_factory=Counter)
    failures: int = 0

    def add(self, rec: dict) -> None:
        with self.lock:
            self.records.append(rec)
            if rec["status"] != "completed":
                self.failures += 1

    def skip(self, reason: str) -> None:
        with self.lock:
            self.skipped[reason] += 1


def file_id(path: Path, root: Path, redact: bool) -> str:
    rel = path.relative_to(root).as_posix()
    if not redact:
        return rel
    return hashlib.sha256(rel.encode()).hexdigest()[:12]


def request(api: str, method: str, path: str, body: bytes | None = None,
            headers: dict | None = None, timeout: float = 300) -> tuple[int, dict, bytes]:
    req = urllib.request.Request(api + path, data=body, method=method, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, dict(r.headers), r.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read()


def auth_headers(token: str) -> dict:
    return {"Authorization": f"Bearer {token}"}


def upload(api: str, token: str, path: Path, mode: str, timeout: float) -> tuple[int, dict]:
    """Stream a multipart body from disk (no full-file buffering in memory)."""
    boundary = "----batch" + uuid.uuid4().hex
    head = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"mode\"\r\n\r\n{mode}\r\n"
            f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{path.name}\"\r\n"
            f"Content-Type: {mimetypes.guess_type(path.name)[0] or 'application/octet-stream'}\r\n\r\n").encode()
    tail = f"\r\n--{boundary}--\r\n".encode()
    size = path.stat().st_size
    host = api.split("://", 1)[1]
    base_path = "/" + host.split("/", 1)[1] if "/" in host else ""
    host = host.split("/", 1)[0]
    netloc, _, prefix = host.partition(":")
    port = int(prefix or 80)
    conn = http.client.HTTPConnection(netloc, port, timeout=timeout)
    try:
        conn.putrequest("POST", base_path + "/v1/jobs")
        conn.putheader("Authorization", f"Bearer {token}")
        conn.putheader("Content-Type", f"multipart/form-data; boundary={boundary}")
        conn.putheader("Content-Length", str(len(head) + size + len(tail)))
        conn.endheaders()
        conn.send(head)
        with open(path, "rb") as f:
            while True:
                chunk = f.read(1 << 20)
                if not chunk:
                    break
                conn.send(chunk)
        conn.send(tail)
        resp = conn.getresponse()
        raw = resp.read()
        status = resp.status
    finally:
        conn.close()
    try:
        return status, json.loads(raw)
    except json.JSONDecodeError:
        return status, {"raw": raw[:200].decode("utf-8", "replace")}


def wait_job(api: str, token: str, job_id: str, timeout: float) -> tuple[dict, float]:
    t0 = time.perf_counter()
    deadline = time.time() + timeout
    while time.time() < deadline:
        status, _, raw = request(api, "GET", f"/v1/jobs/{job_id}", headers=auth_headers(token), timeout=30)
        if status == 200:
            job = json.loads(raw)
            if job.get("status") in ("completed", "failed", "cancelled"):
                return job, (time.perf_counter() - t0) * 1000
        time.sleep(0.5)  # status polling shares the per-owner rate budget
    return {"status": "timeout", "error_code": "client_timeout"}, (time.perf_counter() - t0) * 1000


def parse_one(api: str, token: str, path: Path, root: Path, mode: str,
              redact: bool, stats: Stats, timeout: float) -> None:
    try:
        _parse_one(api, token, path, root, mode, redact, stats, timeout)
    except (ConnectionResetError, BrokenPipeError, urllib.error.URLError, OSError) as e:
        # A rejected/aborted upload can reset the connection; record it and move on
        # instead of failing the whole batch.
        rec = {"file_id": file_id(path, root, redact), "ext": path.suffix.lower(),
               "bytes": path.stat().st_size, "mode": mode, "status": "client_error",
               "error_code": "connection_reset"}
        if not redact:
            rec["error"] = str(e)[:160]
        stats.add(rec)


def _parse_one(api: str, token: str, path: Path, root: Path, mode: str,
               redact: bool, stats: Stats, timeout: float) -> None:
    ext = path.suffix.lower()
    rec = {
        "file_id": file_id(path, root, redact),
        "ext": ext,
        "bytes": path.stat().st_size,
        "mode": mode,
        "requested_at": datetime.now(timezone.utc).isoformat(),
    }
    status, body = upload(api, token, path, mode, timeout)
    if status not in (200, 202):
        rec.update({"status": "rejected", "http": status,
                    "error_code": (body.get("error") or {}).get("code")})
        if not redact:
            rec["error"] = ((body.get("error") or {}).get("message") or "")[:160]
        stats.add(rec)
        return
    job_id = body["job_id"]
    job, client_ms = wait_job(api, token, job_id, timeout)
    rec.update({
        "status": job.get("status"),
        "engine": job.get("engine"),
        "pages": job.get("page_count"),
        "client_ms": round(client_ms, 1),
        "error_code": job.get("error_code"),
        "warnings": job.get("warning_count"),
        "result_version": job.get("result_version"),
    })
    if not redact and job.get("error_message"):
        # Engine stderr is embedded in job errors and can quote the document or its path, so
        # it is only kept when the caller explicitly asked for an unredacted report.
        rec["error"] = job["error_message"][:160]
    if job.get("status") == "completed":
        st, hdr, raw = request(api, "GET", f"/v1/jobs/{job_id}/result?format=json",
                               headers=auth_headers(token), timeout=timeout)
        if st == 200:
            try:
                ir = json.loads(raw)
                m = ir.get("metrics") or {}
                rec["chars"] = m.get("output_chars")
                rec["parse_ms"] = m.get("parse_ms")
                rec["peak_rss_mb"] = m.get("peak_rss_mb")
                rec["tables"] = len(ir.get("tables") or [])
                rec["warn_codes"] = sorted({w.get("code") for w in ir.get("warnings") or []})
                rec["output_sha256"] = hashlib.sha256(raw).hexdigest()[:16]
                rec["bbox"] = bool(ir.get("elements") and
                                   any((e.get("source_location") or {}).get("bbox")
                                       for e in ir["elements"]))
            except (json.JSONDecodeError, MemoryError):
                rec["result_error"] = "unparsable_ir"
        else:
            rec["result_http"] = st
    # Release immediately: the batch only keeps statistics, not artifacts.
    version = job.get("result_version")
    if job.get("status") == "completed" and version:
        request(api, "POST", f"/v1/jobs/{job_id}/ack",
                json.dumps({"result_version": version}).encode(),
                auth_headers(token) | {"Content-Type": "application/json"}, timeout=30)
    stats.add(rec)


def _p50(values: list[float]) -> float | None:
    return round(statistics.median(values), 1) if values else None


def _p95(values: list[float]) -> float | None:
    if not values:
        return None
    v = sorted(values)
    return round(v[max(0, int(len(v) * 0.95) - 1)], 1)


def summarize(records: list[dict], skipped: Counter) -> dict:
    by_ext = defaultdict(lambda: {"n": 0, "ok": 0, "failed": 0, "chars": 0, "tables": 0,
                                  "engines": Counter(), "codes": Counter(), "ms": []})
    for r in records:
        b = by_ext[r["ext"]]
        b["n"] += 1
        if r["status"] == "completed":
            b["ok"] += 1
            b["chars"] += r.get("chars") or 0
            b["tables"] += r.get("tables") or 0
            if r.get("engine"):
                b["engines"][r["engine"]] += 1
        else:
            b["failed"] += 1
            if r.get("error_code"):
                b["codes"][r["error_code"]] += 1
        if r.get("client_ms"):
            b["ms"].append(r["client_ms"])
    out = {}
    for ext, b in sorted(by_ext.items(), key=lambda kv: -kv[1]["n"]):
        ms = sorted(b["ms"])
        out[ext] = {
            "files": b["n"], "completed": b["ok"], "failed": b["failed"],
            "completion_rate": round(b["ok"] / b["n"], 3),
            "chars_total": b["chars"], "tables_total": b["tables"],
            "engines": dict(b["engines"]), "error_codes": dict(b["codes"]),
            "client_ms_p50": round(statistics.median(ms), 1) if ms else None,
            "client_ms_p95": round(ms[max(0, int(len(ms) * 0.95) - 1)], 1) if ms else None,
            "parse_ms_p50": _p50([r["parse_ms"] for r in records
                                  if r["ext"] == ext and r.get("parse_ms")]),
            "parse_ms_p95": _p95([r["parse_ms"] for r in records
                                  if r["ext"] == ext and r.get("parse_ms")]),
        }
    all_ms = sorted(r["client_ms"] for r in records if r.get("client_ms"))
    return {
        "files_considered": len(records),
        "completed": sum(1 for r in records if r["status"] == "completed"),
        "failed": sum(1 for r in records if r["status"] != "completed"),
        "skipped": dict(skipped),
        "by_ext": out,
        "client_ms_p50": round(statistics.median(all_ms), 1) if all_ms else None,
        "client_ms_p95": round(all_ms[max(0, int(len(all_ms) * 0.95) - 1)], 1) if all_ms else None,
        "empty_or_tiny_output": sum(1 for r in records
                                    if r["status"] == "completed" and (r.get("chars") or 0) < 20),
        "ocr_or_scan_warnings": sum(1 for r in records
                                    if any(c in (r.get("warn_codes") or [])
                                           for c in ("ocr_low_confidence", "no_text_layer",
                                                     "no_page_boundaries"))),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True)
    ap.add_argument("--api", default="http://127.0.0.1:18090")
    ap.add_argument("--token", default="batch-token")
    ap.add_argument("--mode", default="auto", choices=["auto", "fast", "accurate"])
    ap.add_argument("--concurrency", type=int, default=4)
    ap.add_argument("--limit", type=int, default=0, help="max files (0 = all)")
    ap.add_argument("--max-mb", type=int, default=0, help="skip files larger than this (0 = no limit)")
    ap.add_argument("--timeout", type=float, default=600)
    ap.add_argument("--out", required=True)
    ap.add_argument("--no-redact", action="store_true")
    ap.add_argument("--only-ext", default="", help="comma list, e.g. .pdf,.docx")
    args = ap.parse_args()

    root = Path(args.dir)
    files = []
    only = {e if e.startswith(".") else "." + e for e in args.only_ext.split(",") if e}
    skipped = Counter()
    for p in sorted(root.rglob("*")):
        if not p.is_file():
            continue
        ext = p.suffix.lower()
        if p.name.startswith("~$"):
            skipped["office_lock_file"] += 1
            continue
        if ext not in ACCEPTED_EXT:
            skipped[f"unsupported{ext or '(no-ext)'}"] += 1
            continue
        if only and ext not in only:
            skipped["filtered_by_only_ext"] += 1
            continue
        if args.max_mb and p.stat().st_size > args.max_mb << 20:
            skipped["over_max_mb"] += 1
            continue
        files.append(p)
    if args.limit:
        files = files[: args.limit]

    print(f"parsing {len(files)} files from {root} (mode={args.mode}, concurrency={args.concurrency})")
    stats = Stats()
    t0 = time.perf_counter()
    with ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        futures = [pool.submit(parse_one, args.api, args.token, p, root, args.mode,
                               not args.no_redact, stats, args.timeout) for p in files]
        done = 0
        for f in futures:
            f.result()
            done += 1
            if done % 25 == 0:
                print(f"  {done}/{len(files)} done ({time.perf_counter()-t0:.0f}s)", flush=True)

    elapsed = time.perf_counter() - t0
    summary = summarize(stats.records, skipped)
    summary["wall_seconds"] = round(elapsed, 1)
    summary["files_per_second"] = round(len(files) / elapsed, 2) if elapsed else None
    summary["mode"] = args.mode
    summary["redacted"] = not args.no_redact
    summary["generated_at"] = datetime.now(timezone.utc).isoformat()

    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps({"summary": summary, "records": stats.records}, indent=1),
                   encoding="utf-8")
    print(json.dumps(summary, indent=2))
    print(f"\nreport: {out.name} (in {out.parent})" if args.no_redact else f"\nreport: {out.name}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
