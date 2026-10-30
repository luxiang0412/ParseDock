#!/usr/bin/env python3
"""ParseDock Phase 0 acceptance harness: T01-T16.

Every case spins up real processes (coordinator / API replicas / workers) over
HTTP and asserts observable behaviour only. Raw evidence (statuses, headers,
filesystem state, logs) is written to
    benchmark/results/raw/acceptance-<timestamp>.json
and a human summary to
    benchmark/results/summary/acceptance.json

Usage:
  python tests/integration/acceptance.py --profile lifecycle
  python tests/integration/acceptance.py --profile real --engines native,xberg

The lifecycle profile uses the deterministic `mock` engine so ACK/TTL/lease
fencing can be exercised without models. The real profile exercises actual
parsers for T01/T02/T11/T12 and skips the fault-injection cases that are
already covered by the lifecycle run.
"""
from __future__ import annotations

import argparse
import hashlib
import http.client
import json
import os
import random
import shutil
import signal
import socket
import sqlite3
import string
import subprocess
import sys
import time
import traceback
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
BIN = ROOT / "runtime" / "bin" / "docparse"
WORK = ROOT / "runtime" / "acceptance"
LOGS = ROOT / "runtime" / "logs" / "acceptance"
FIX = ROOT / "benchmark" / "fixtures" / "generated"
BAD = ROOT / "benchmark" / "fixtures" / "invalid"
RAW_DIR = ROOT / "benchmark" / "results" / "raw"
SUMFILE = ROOT / "benchmark" / "results" / "summary" / "acceptance.json"

API_TOKEN = "itest-api-token"
OWNER = "itest-owner"
INTERNAL_TOKEN = "itest-internal"


# --------------------------------------------------------------------------- utils
def utcnow() -> str:
    return datetime.now(timezone.utc).isoformat()


_ISSUED_PORTS: set[int] = set()


def free_port() -> int:
    "Allocate a unique ephemeral port (never reuse one already handed out)."
    for _ in range(100):
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        if port not in _ISSUED_PORTS:
            _ISSUED_PORTS.add(port)
            return port
    raise RuntimeError("could not allocate a unique port")


def port_listening(port: int) -> bool:
    with socket.socket() as s:
        s.settimeout(0.3)
        return s.connect_ex(("127.0.0.1", port)) == 0


def wait_port_free(port: int, timeout: float = 20) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if not port_listening(port):
            return
        time.sleep(0.2)
    raise RuntimeError(f"port {port} still in use")


def kill_pid(pid: int, sig: int = signal.SIGKILL) -> None:
    try:
        os.kill(pid, sig)
    except ProcessLookupError:
        pass


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


class Resp:
    def __init__(self, status: int, headers: dict[str, str], body: bytes):
        self.status = status
        self.headers = {k.lower(): v for k, v in headers.items()}
        self.body = body

    def json(self) -> Any:
        try:
            return json.loads(self.body.decode("utf-8", "replace"))
        except Exception:
            return None

    def text(self) -> str:
        return self.body.decode("utf-8", "replace")


def http_request(port: int, method: str, path: str, body: bytes | None = None,
                 headers: dict[str, str] | None = None, timeout: float = 120,
                 read_limit: int | None = None) -> Resp:
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)
    try:
        conn.request(method, path, body=body, headers=headers or {})
        r = conn.getresponse()
        data = r.read(read_limit) if read_limit else r.read()
        return Resp(r.status, dict(r.getheaders()), data)
    finally:
        conn.close()


def multipart(fields: dict[str, str], files: list[tuple[str, Path]]) -> tuple[bytes, str]:
    boundary = "----parsedock" + "".join(random.choices(string.ascii_letters + string.digits, k=18))
    out = bytearray()
    for k, v in fields.items():
        out += f"--{boundary}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n".encode()
    for field_name, path in files:
        name = path.name
        out += (f"--{boundary}\r\nContent-Disposition: form-data; name=\"{field_name}\"; "
                f"filename=\"{name}\"\r\nContent-Type: application/octet-stream\r\n\r\n").encode()
        out += path.read_bytes() + b"\r\n"
    out += f"--{boundary}--\r\n".encode()
    return bytes(out), f"multipart/form-data; boundary={boundary}"


# --------------------------------------------------------------------------- stack
@dataclass
class Stack:
    name: str
    engines: str = "mock"
    worker_env: dict[str, str] = field(default_factory=dict)
    api_count: int = 1
    worker_count: int = 1
    coord_args: list[str] = field(default_factory=list)
    worker_args: list[str] = field(default_factory=list)
    api_args: list[str] = field(default_factory=list)
    api_keys_extra: list[str] = field(default_factory=list)
    keys_file: bool = False
    lanes: str = "PARSEDOCK"
    data_dir_override: Path | None = None

    data_dir: Path = field(init=False)
    port_coord: int = field(init=False)
    ports_api: list[int] = field(init=False, default_factory=list)
    procs: list[subprocess.Popen] = field(init=False, default_factory=list)
    coord: subprocess.Popen | None = field(init=False, default=None)
    by_key: dict[str, subprocess.Popen] = field(init=False, default_factory=dict)
    logs: dict[str, Path] = field(init=False, default_factory=dict)

    def __post_init__(self):
        self.data_dir = self.data_dir_override or (WORK / f"{self.name}-{os.getpid()}")
        self.port_coord = free_port()
        self.ports_api = [free_port() for _ in range(self.api_count)]

    # -- process management
    def _spawn(self, key: str, args: list[str], env: dict[str, str] | None = None) -> subprocess.Popen:
        LOGS.mkdir(parents=True, exist_ok=True)
        log = LOGS / f"{self.name}-{key}-{len(self.procs)}.log"
        self.logs[key] = log
        fh = open(log, "wb")
        e = {**os.environ, **(env or {})}
        p = subprocess.Popen([str(BIN), *args], stdout=fh, stderr=subprocess.STDOUT,
                             env=e, start_new_session=True)
        p._logfile = fh  # type: ignore[attr-defined]
        self.procs.append(p)
        unique = key
        n = 1
        while unique in self.by_key:
            n += 1
            unique = f"{key}{n}"
        self.by_key[unique] = p
        return p

    def start(self, wipe: bool = True) -> "Stack":
        if wipe and self.data_dir.exists():
            shutil.rmtree(self.data_dir)
        self.data_dir.mkdir(parents=True, exist_ok=True)
        coord = [
            "--role", "coordinator",
            "--internal-listen", f"127.0.0.1:{self.port_coord}",
            "--data-dir", str(self.data_dir),
            "--internal-token", INTERNAL_TOKEN,
            "--engines", self.engines,
            "--cleanup-interval", "1s",
            *self.coord_args,
        ]
        self.coord = self._spawn("coordinator", coord)
        key_args = ["--api-keys", ",".join([f"{API_TOKEN}:{OWNER}", *self.api_keys_extra])]
        if self.keys_file:
            keys_path = self.data_dir / "api-keys"
            keys_path.write_text("\n".join([f"{API_TOKEN}:{OWNER}", *self.api_keys_extra]) + "\n",
                                 encoding="utf-8")
            keys_path.chmod(0o600)
            key_args = ["--api-keys-file", str(keys_path)]
        for i, port in enumerate(self.ports_api):
            self._spawn(f"api{i}", [
                "--role", "api",
                "--listen", f"127.0.0.1:{port}",
                "--coordinator-url", f"http://127.0.0.1:{self.port_coord}",
                "--internal-token", INTERNAL_TOKEN,
                *key_args,
                *self.api_args,
            ])
        for i in range(self.worker_count):
            wargs = [
                "--role", "worker",
                "--coordinator-url", f"http://127.0.0.1:{self.port_coord}",
                "--internal-token", INTERNAL_TOKEN,
                "--engines", self.engines,
                "--worker-id", f"{self.name}-w{i}",
                "--worker-concurrency", "1",
                "--work-dir", str(self.data_dir / "work"),
                "--heartbeat", "1s",
                *self.worker_args,
            ]
            self._spawn(f"worker{i}", wargs, env=self.worker_env)
        self.wait_ready()
        return self

    def wait_ready(self, timeout: float = 25) -> None:
        deadline = time.time() + timeout
        while time.time() < deadline:
            try:
                r = http_request(self.ports_api[0], "GET", "/readyz", timeout=2)
                if r.status == 200:
                    return
            except Exception:
                pass
            time.sleep(0.2)
        raise RuntimeError(f"stack {self.name} not ready: {self._tail_logs()}")

    def _tail_logs(self) -> str:
        out = []
        for key, path in self.logs.items():
            try:
                out.append(f"--- {key} ---\n" + path.read_text(errors="replace")[-1500:])
            except Exception:
                pass
        return "\n".join(out)

    def stop(self) -> None:
        for p in self.procs:
            try:
                os.killpg(os.getpgid(p.pid), signal.SIGTERM)
            except Exception:
                try:
                    p.terminate()
                except Exception:
                    pass
        for p in self.procs:
            try:
                p.wait(timeout=10)
            except Exception:
                try:
                    os.killpg(os.getpgid(p.pid), signal.SIGKILL)
                except Exception:
                    pass
            fh = getattr(p, "_logfile", None)
            if fh:
                fh.close()
        self.procs.clear()

    def __enter__(self):
        return self.start()

    def __exit__(self, *exc):
        self.stop()

    # -- API helpers
    @property
    def api(self) -> int:
        return self.ports_api[0]

    def api_at(self, idx: int) -> int:
        return self.ports_api[idx]

    def headers(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        h = {"Authorization": f"Bearer {API_TOKEN}"}
        if extra:
            h.update(extra)
        return h

    def create_job(self, path: Path, fields: dict[str, str] | None = None,
                   port: int | None = None, extra_headers: dict[str, str] | None = None) -> Resp:
        body, ctype = multipart(fields or {}, [("file", path)])
        h = self.headers({"Content-Type": ctype, **(extra_headers or {})})
        return http_request(port or self.api, "POST", "/v1/jobs", body, h)

    def get_job(self, job_id: str, port: int | None = None) -> Resp:
        return http_request(port or self.api, "GET", f"/v1/jobs/{job_id}", headers=self.headers())

    def get_result(self, job_id: str, fmt: str = "markdown", port: int | None = None,
                   timeout: float = 120) -> Resp:
        return http_request(port or self.api, "GET", f"/v1/jobs/{job_id}/result?format={fmt}",
                            headers=self.headers(), timeout=timeout)

    def ack(self, job_id: str, version: str | None, port: int | None = None,
            extra_headers: dict[str, str] | None = None) -> Resp:
        body = json.dumps({"result_version": version, "action": "release"}).encode()
        return http_request(port or self.api, "POST", f"/v1/jobs/{job_id}/ack", body,
                            self.headers({"Content-Type": "application/json", **(extra_headers or {})}))

    def cancel(self, job_id: str, port: int | None = None) -> Resp:
        return http_request(port or self.api, "POST", f"/v1/jobs/{job_id}/cancel", b"", self.headers())

    def wait_status(self, job_id: str, want: set[str], timeout: float = 60) -> dict:
        deadline = time.time() + timeout
        last = {}
        while time.time() < deadline:
            r = self.get_job(job_id)
            last = r.json() or {}
            if last.get("status") in want:
                return last
            time.sleep(0.15)
        raise AssertionError(f"job {job_id} did not reach {want}; last={last}")

    def wait_retention(self, job_id: str, want: set[str], timeout: float = 60) -> dict:
        deadline = time.time() + timeout
        last = {}
        while time.time() < deadline:
            r = self.get_job(job_id)
            last = r.json() or {}
            if last.get("retention_status") in want:
                return last
            time.sleep(0.15)
        raise AssertionError(f"job {job_id} retention not in {want}; last={last}")

    def kill_coordinator(self, sig: int = signal.SIGKILL) -> None:
        """Kill only the coordinator process (never the API replicas)."""
        p = self.coord
        if p is None:
            return
        kill_pid(p.pid, sig)
        try:
            p.wait(timeout=15)
        except Exception:
            pass
        if p in self.procs:
            self.procs.remove(p)
        self.coord = None

    def restart_coordinator(self, extra_args: list[str] | None = None,
                            attempts: int = 12) -> None:
        """Kill the coordinator (if alive) and start a new one on the same port.

        Binding can race with the old listener's teardown, so this retries
        instead of assuming an instant release.
        """
        self.kill_coordinator()
        args = [
            "--role", "coordinator",
            "--internal-listen", f"127.0.0.1:{self.port_coord}",
            "--data-dir", str(self.data_dir),
            "--internal-token", INTERNAL_TOKEN,
            "--engines", self.engines,
            "--cleanup-interval", "1s",
            *(extra_args or []),
        ]
        last = ""
        for i in range(attempts):
            p = self._spawn(f"coordinator-restart{i}", args)
            deadline = time.time() + 5
            while time.time() < deadline:
                if p.poll() is not None:
                    last = self.logs[f"coordinator-restart{i}"].read_text(errors="replace")[-400:]
                    break
                try:
                    r = http_request(self.port_coord, "GET", "/healthz", timeout=1)
                    if r.status == 200:
                        self.coord = p
                        return
                except Exception:
                    pass
                time.sleep(0.15)
            kill_pid(p.pid, signal.SIGKILL)
            try:
                p.wait(timeout=5)
            except Exception:
                pass
            time.sleep(0.5)
        raise RuntimeError(f"coordinator did not restart: {last}")

    def db(self) -> sqlite3.Connection:
        conn = sqlite3.connect(self.data_dir / "coordinator.db", timeout=10)
        conn.row_factory = sqlite3.Row
        return conn

    def stored_files(self, job_id: str) -> dict[str, int]:
        out = {}
        for sub in ("inputs", "results"):
            d = self.data_dir / sub / job_id
            if d.exists():
                for p in d.rglob("*"):
                    if p.is_file():
                        out[str(p.relative_to(self.data_dir))] = p.stat().st_size
        return out

    def internal(self, method: str, path: str, payload: Any = None,
                 headers: dict[str, str] | None = None) -> Resp:
        body = json.dumps(payload).encode() if payload is not None else None
        h = {"X-Internal-Token": INTERNAL_TOKEN, "Content-Type": "application/json"}
        if headers:
            h.update(headers)
        return http_request(self.port_coord, method, path, body, h)


# --------------------------------------------------------------------------- cases
@dataclass
class Case:
    id: str
    name: str
    status: str = "SKIP"
    detail: dict = field(default_factory=dict)
    error: str = ""

    def as_dict(self) -> dict:
        return {"id": self.id, "name": self.name, "status": self.status,
                "detail": self.detail, "error": self.error}


def case(cid: str, name: str):
    def deco(fn):
        fn._case = (cid, name)
        return fn
    return deco


@case("T01", "real upload+parse of a PDF produces a correct, readable result")
def t01(stack: Stack) -> dict:
    src = FIX / "pdf_text_en_3p.pdf"
    r = stack.create_job(src)
    assert r.status == 202, f"create returned {r.status}: {r.text()[:300]}"
    job = r.json()
    jid = job["job_id"]
    final = stack.wait_status(jid, {"completed"}, timeout=180)
    res = stack.get_result(jid, "markdown")
    assert res.status == 200, f"result {res.status}: {res.text()[:200]}"
    md = res.text()
    jres = stack.get_result(jid, "json")
    assert jres.status == 200
    ir = jres.json()
    assert ir["schema_version"] == "doc-ir/0.1"
    assert ir["parser_engine"], "engine not recorded"
    assert "Quarterly Logistics Review" in md, "expected title missing from markdown"
    assert "128,400" in md, "expected figure missing from markdown"
    assert ir["page_count"] == 3, f"page count {ir['page_count']}"
    has_tables = len(ir.get("tables") or []) > 0
    return {
        "job_id": jid, "engine": final.get("engine"), "result_version": final.get("result_version"),
        "markdown_bytes": len(res.body), "elements": len(ir["elements"]),
        "tables": len(ir.get("tables") or []), "table_recovered": has_tables,
        "warnings": len(ir.get("warnings") or []), "etag": res.headers.get("etag"),
        "content_length_matches": int(res.headers.get("content-length", 0)) == len(res.body),
        "markdown_head": md[:200],
    }


@case("T02", "core formats parse or fail with an explicit unsupported error")
def t02(stack: Stack) -> dict:
    samples = [
        ("docx", FIX / "docx_report.docx"),
        ("xlsx", FIX / "xlsx_workbook.xlsx"),
        ("csv", FIX / "csv_utf8.csv"),
        ("md", FIX / "md_notes.md"),
        ("png", FIX / "image_png.png") if (FIX / "image_png.png").exists() else ("png", FIX / "image_screenshot.jpg"),
    ]
    out = {}
    for label, path in samples:
        r = stack.create_job(path)
        if r.status != 202:
            out[label] = {"create_status": r.status, "error": (r.json() or {}).get("error")}
            continue
        jid = r.json()["job_id"]
        try:
            final = stack.wait_status(jid, {"completed", "failed"}, timeout=180)
        except AssertionError as e:
            out[label] = {"status": "timeout", "error": str(e)}
            continue
        entry = {"job_id": jid, "status": final.get("status"), "engine": final.get("engine"),
                 "error_code": final.get("error_code")}
        if final.get("status") == "completed":
            md = stack.get_result(jid, "markdown")
            entry["markdown_bytes"] = len(md.body)
            entry["has_text"] = bool(md.text().strip())
        out[label] = entry
    # Explicit rejection of a non-whitelisted type.
    weird = BAD / "not_a_docx.docx"
    r = stack.create_job(weird)
    out["unsupported"] = {"create_status": r.status, "code": ((r.json() or {}).get("error") or {}).get("code")}
    assert out["unsupported"]["create_status"] in (415, 422), out["unsupported"]
    completed = [k for k, v in out.items() if v.get("status") == "completed"]
    assert completed, f"no format completed: {out}"
    return out


@case("T03", "ACK releases the result and later downloads return 410")
def t03(stack: Stack) -> dict:
    r = stack.create_job(FIX / "csv_utf8.csv")
    jid = r.json()["job_id"]
    final = stack.wait_status(jid, {"completed"}, timeout=60)
    ver = final["result_version"]
    before = stack.stored_files(jid)
    a = stack.ack(jid, ver, extra_headers={"Idempotency-Key": "ack-t03"})
    assert a.status == 202, f"ack {a.status}: {a.text()[:300]}"
    assert a.json()["retention_status"] == "release_pending"
    blocked = stack.get_result(jid)
    assert blocked.status == 410, f"download after ack returned {blocked.status}"
    released = stack.wait_retention(jid, {"released"}, timeout=30)
    after = stack.stored_files(jid)
    assert not after, f"files remain after release: {after}"
    gone = stack.get_result(jid)
    assert gone.status == 410
    return {"job_id": jid, "result_version": ver, "files_before": before, "files_after": after,
            "ack_http": a.status, "post_ack_download": blocked.status, "final_retention": released["retention_status"]}


@case("T04", "no ACK: TTL expires the result, 410 inside tombstone, metadata purged later")
def t04(stack: Stack) -> dict:
    r = stack.create_job(FIX / "txt_plain.txt", fields={"ttl_seconds": "2"})
    jid = r.json()["job_id"]
    final = stack.wait_status(jid, {"completed"}, timeout=60)
    first = stack.get_result(jid)
    assert first.status == 200, "result should be readable before TTL"
    expired = stack.wait_retention(jid, {"expired"}, timeout=45)
    gone = stack.get_result(jid)
    assert gone.status == 410, f"expected 410 after expiry, got {gone.status}"
    assert not stack.stored_files(jid), "files not deleted after expiry"
    # tombstone purge
    deadline = time.time() + 60
    purged = False
    while time.time() < deadline:
        if stack.get_job(jid).status == 404:
            purged = True
            break
        time.sleep(0.5)
    return {"job_id": jid, "result_expires_at": final.get("result_expires_at"),
            "first_download": first.status, "after_ttl": gone.status,
            "retention": expired["retention_status"], "tombstone_purged_404": purged,
            "files_left": stack.stored_files(jid)}


@case("T05", "ACK during an active download waits for the download lease")
def t05(stack: Stack) -> dict:
    r = stack.create_job(FIX / "txt_plain.txt")
    jid = r.json()["job_id"]
    final = stack.wait_status(jid, {"completed"}, timeout=120)
    ver = final["result_version"]
    assert stack.stored_files(jid), "expected result files"

    # Hold a slow download open: read a few bytes, keep the socket alive.
    conn = http.client.HTTPConnection("127.0.0.1", stack.api, timeout=120)
    conn.request("GET", f"/v1/jobs/{jid}/result?format=markdown", headers=stack.headers())
    resp = conn.getresponse()
    assert resp.status == 200
    head = resp.read(1024)
    lease_id = resp.getheader("X-Download-Lease")

    a = stack.ack(jid, ver)
    assert a.status == 202
    files_during = stack.stored_files(jid)
    # A new download must be refused while retention has begun.
    second = stack.get_result(jid)
    # Cleanup must skip the job while the lease is live.
    time.sleep(3)
    files_held = stack.stored_files(jid)
    assert files_held, "cleaner deleted files while a download lease was active"
    # Finish the download and let the lease expire.
    rest = resp.read()
    conn.close()
    assert len(head) + len(rest) > 0
    released = stack.wait_retention(jid, {"released"}, timeout=60)
    assert not stack.stored_files(jid), "files remain after lease expiry and cleanup"
    return {"job_id": jid, "lease_id": lease_id, "first_download_bytes": len(head) + len(rest),
            "ack_http": a.status, "second_download_http": second.status,
            "files_during_ack": files_during, "final_retention": released["retention_status"]}


@case("T06", "repeated ACK is idempotent and does not touch other jobs")
def t06(stack: Stack) -> dict:
    a = stack.create_job(FIX / "csv_utf8.csv").json()["job_id"]
    b = stack.create_job(FIX / "tsv_data.tsv").json()["job_id"]
    fa = stack.wait_status(a, {"completed"}, timeout=60)
    fb = stack.wait_status(b, {"completed"}, timeout=60)
    r1 = stack.ack(a, fa["result_version"])
    r2 = stack.ack(a, fa["result_version"])
    r3 = stack.ack(a, "sha256:deadbeef")
    assert r1.status == 202 and r2.status == 202, (r1.status, r2.status)
    stack.wait_retention(a, {"released"}, timeout=30)
    untouched = stack.get_result(b)
    assert untouched.status == 200, f"other job affected: {untouched.status}"
    assert fb["result_version"] == stack.get_job(b).json()["result_version"]
    return {"job_a": a, "job_b": b, "ack1": r1.status, "ack2": r2.status,
            "ack_stale_version": r3.status, "other_job_download": untouched.status}


@case("T07", "ACK racing TTL yields exactly one final state and no orphan files")
def t07(stack: Stack) -> dict:
    results = []
    for _ in range(4):
        r = stack.create_job(FIX / "txt_plain.txt", fields={"ttl_seconds": "2"})
        jid = r.json()["job_id"]
        final = stack.wait_status(jid, {"completed"}, timeout=60)
        ver = final["result_version"]
        # Race: ACK at the same moment the TTL fires.
        time.sleep(1.9)
        stack.ack(jid, ver)
        job = stack.wait_retention(jid, {"released", "expired"}, timeout=45)
        results.append({"job_id": jid, "final": job["retention_status"],
                        "files": stack.stored_files(jid)})
    assert all(not x["files"] for x in results), results
    assert all(x["final"] in ("released", "expired") for x in results), results
    return {"races": results}


@case("T08", "SIGKILL during cleanup: restart finishes the job with no orphan data")
def t08(stack: Stack) -> dict:
    jids = []
    for _ in range(3):
        r = stack.create_job(FIX / "csv_utf8.csv")
        jid = r.json()["job_id"]
        final = stack.wait_status(jid, {"completed"}, timeout=60)
        stack.ack(jid, final["result_version"])
        jids.append(jid)
    # Kill the coordinator before the 1s cleaner can finish.
    stack.kill_coordinator()
    files_after_kill = {j: stack.stored_files(j) for j in jids}

    # Restart against the same data dir/db.
    stack.restart_coordinator()
    time.sleep(4)
    files_final = {j: stack.stored_files(j) for j in jids}
    retentions = {}
    for j in jids:
        retentions[j] = stack.get_job(j).json().get("retention_status")
    assert all(not v for v in files_final.values()), files_final
    assert all(v in ("released",) for v in retentions.values()), retentions
    return {"jobs": jids, "files_after_sigkill": files_after_kill,
            "files_after_restart": files_final, "retention_after_restart": retentions}


@case("T09", "worker SIGKILL: lease expires, job retries, stale token cannot submit")
def t09(stack: Stack) -> dict:
    r = stack.create_job(FIX / "txt_plain.txt")
    jid = r.json()["job_id"]
    # Wait until the (single) worker has claimed it.
    deadline = time.time() + 30
    token = None
    while time.time() < deadline:
        with stack.db() as db:
            row = db.execute("SELECT lease_token, attempt_no, state FROM attempts WHERE job_id=?", (jid,)).fetchone()
        if row and row["state"] == "running":
            token = row["lease_token"]
            break
        time.sleep(0.1)
    assert token, "worker never claimed the job"
    victim = stack.by_key["worker0"]
    kill_pid(victim.pid, signal.SIGKILL)
    victim.wait(timeout=10)
    # Restart a fresh worker to pick up the requeued attempt.
    stack._spawn("worker-restart", [
        "--role", "worker",
        "--coordinator-url", f"http://127.0.0.1:{stack.port_coord}",
        "--internal-token", INTERNAL_TOKEN,
        "--engines", stack.engines,
        "--worker-id", "restarted-worker",
        "--work-dir", str(stack.data_dir / "work"),
        "--heartbeat", "1s",
    ], env=stack.worker_env)
    final = stack.wait_status(jid, {"completed"}, timeout=120)
    # The stale attempt must not be able to publish a result.
    stale = stack.internal("PUT", f"/internal/tasks/{jid}/result", {
        "lease_token": token, "engine": "mock", "engine_version": "mock-1.0",
        "device_type": "cpu", "page_count": 1,
        "document": {"schema_version": "doc-ir/0.1", "elements": [], "warnings": []},
    })
    stale_hb = stack.internal("POST", f"/internal/tasks/{jid}/heartbeat",
                              {"lease_token": token, "progress_percent": 50, "stage": "stale"})
    with stack.db() as db:
        attempts = [dict(r) for r in db.execute(
            "SELECT attempt_no, state FROM attempts WHERE job_id=? ORDER BY attempt_no", (jid,))]
    assert final["status"] == "completed"
    assert stale.status == 409, f"stale result accepted: {stale.status} {stale.text()[:200]}"
    assert stale_hb.status == 409, f"stale heartbeat accepted: {stale_hb.status}"
    return {"job_id": jid, "attempts": attempts, "stale_result_http": stale.status,
            "stale_heartbeat_http": stale_hb.status, "final_status": final["status"],
            "result_version": final["result_version"]}


@case("T10", "two API replicas: any replica can query, download and ACK the same job")
def t10(stack: Stack) -> dict:
    assert len(stack.ports_api) >= 2
    r = stack.create_job(FIX / "pdf_text_en_3p.pdf", port=stack.api_at(0))
    jid = r.json()["job_id"]
    final = stack.wait_status(jid, {"completed"}, timeout=120)
    q_other = stack.get_job(jid, port=stack.api_at(1))
    dl_other = stack.get_result(jid, port=stack.api_at(1))
    assert q_other.status == 200 and dl_other.status == 200
    assert q_other.json()["result_version"] == final["result_version"]
    # SSE via replica 2 with Last-Event-ID resumption.
    events = []
    conn = http.client.HTTPConnection("127.0.0.1", stack.api_at(1), timeout=10)
    conn.request("GET", f"/v1/jobs/{jid}/events", headers=stack.headers())
    resp = conn.getresponse()
    ctype = resp.getheader("Content-Type", "")
    deadline = time.time() + 5
    buf = b""
    while time.time() < deadline:
        chunk = resp.read(1)
        if not chunk:
            break
        buf += chunk
        if b"stream_end" in buf or b"completed" in buf:
            break
    conn.close()
    events = [line for line in buf.decode(errors="replace").splitlines() if line.startswith("event:")]
    ack_other = stack.ack(jid, final["result_version"], port=stack.api_at(1))
    assert ack_other.status == 202
    return {"job_id": jid, "create_port": stack.api_at(0), "query_port": stack.api_at(1),
            "sse_content_type": ctype, "sse_events": events[:6],
            "ack_port": stack.api_at(1), "ack_http": ack_other.status}


@case("T11", "CPU-only node: no GPU/CUDA dependency in the parse path")
def t11(stack: Stack) -> dict:
    r = stack.create_job(FIX / "pdf_text_en_3p.pdf", fields={"device": "cpu"})
    jid = r.json()["job_id"]
    final = stack.wait_status(jid, {"completed"}, timeout=180)
    res = stack.get_result(jid, "json")
    ir = res.json()
    return {"job_id": jid, "engine": final.get("engine"), "requested_device": "cpu",
            "actual_device": ir["routing"]["actual_device"], "status": final["status"]}


@case("T12", "device=gpu_required without a GPU worker is an explicit failure, never silent CPU")
def t12(stack: Stack) -> dict:
    r = stack.create_job(FIX / "pdf_text_en_3p.pdf", fields={"device": "gpu_required"})
    body = r.json() or {}
    code = (body.get("error") or {}).get("code")
    detail = {"create_status": r.status, "error_code": code}
    if r.status == 202:
        jid = body["job_id"]
        final = stack.wait_status(jid, {"completed", "failed"}, timeout=60)
        detail.update({"job_id": jid, "status": final["status"], "engine": final.get("engine"),
                       "error_code": final.get("error_code")})
        assert final.get("status") != "completed" or final.get("engine"), detail
    else:
        assert r.status == 409 and code == "gpu_unavailable", detail
        # No new job row may have been created by the rejected request.
        with stack.db() as db:
            n = db.execute("SELECT COUNT(*) c FROM jobs WHERE requested_device='gpu_required'").fetchone()["c"]
        detail["gpu_jobs_persisted"] = n
        assert n == 0, "gpu_required rejection still persisted a job"
    return detail


@case("T13", "invalid and adversarial uploads are rejected without crashing")
def t13(stack: Stack) -> dict:
    cases = {
        "empty": BAD / "empty.pdf",
        "corrupt": BAD / "corrupt.pdf",
        "wrong_ext": BAD / "renamed_docx.pdf",
        "password": BAD / "password.pdf",
        "zipbomb": BAD / "zipbomb.docx",
        "fake_zip": BAD / "not_a_docx.docx",
    }
    out = {}
    for name, path in cases.items():
        r = stack.create_job(path)
        body = r.json() or {}
        out[name] = {"http": r.status, "code": (body.get("error") or {}).get("code")}
        assert 400 <= r.status < 500, f"{name} was not rejected: {r.status} {r.text()[:200]}"
    # Server must still work afterwards.
    ok = stack.create_job(FIX / "csv_utf8.csv")
    assert ok.status == 202
    jid = ok.json()["job_id"]
    stack.wait_status(jid, {"completed"}, timeout=60)
    out["still_alive"] = True
    return out


@case("T14", "disk watermark blocks new uploads but keeps live results readable")
def t14(stack: Stack) -> dict:
    # Phase 1: normal watermark, produce a result we must not lose.
    r = stack.create_job(FIX / "pdf_text_en_3p.pdf")
    jid = r.json()["job_id"]
    final = stack.wait_status(jid, {"completed"}, timeout=180)
    ver = final["result_version"]

    # Phase 2: restart the coordinator with an impossible watermark.
    stack.restart_coordinator(["--min-free-gb", "1000000"])
    time.sleep(1)
    blocked = stack.create_job(FIX / "csv_utf8.csv")
    still_readable = stack.get_result(jid, "markdown")
    # Restore a healthy coordinator so later cases are unaffected.
    stack.restart_coordinator()
    recovered = stack.create_job(FIX / "txt_plain.txt")
    return {"existing_job": jid, "result_version": ver,
            "upload_after_restore_http": recovered.status,
            "upload_http": blocked.status, "upload_code": ((blocked.json() or {}).get("error") or {}).get("code"),
            "existing_download_http": still_readable.status,
            "existing_bytes": len(still_readable.body)}


@case("T15", "stale attempt writes are rejected by lease fencing")
def t15(stack: Stack) -> dict:
    r = stack.create_job(FIX / "txt_plain.txt")
    jid = r.json()["job_id"]
    deadline = time.time() + 30
    token = None
    while time.time() < deadline:
        with stack.db() as db:
            row = db.execute("SELECT lease_token, state FROM attempts WHERE job_id=?", (jid,)).fetchone()
        if row and row["state"] == "running":
            token = row["lease_token"]
            break
        time.sleep(0.1)
    assert token, "no running attempt"
    stack.wait_status(jid, {"completed"}, timeout=60)
    late_result = stack.internal("PUT", f"/internal/tasks/{jid}/result", {
        "lease_token": token, "engine": "mock", "engine_version": "mock-1.0",
        "device_type": "cpu", "page_count": 9,
        "document": {"schema_version": "doc-ir/0.1", "elements": [], "warnings": []},
    })
    late_fail = stack.internal("POST", f"/internal/tasks/{jid}/fail",
                               {"lease_token": token, "code": "internal_error", "message": "late", "retryable": False})
    after = stack.get_job(jid).json()
    return {"job_id": job_id(jid), "late_result_http": late_result.status,
            "late_fail_http": late_fail.status, "result_version_unchanged": after["result_version"],
            "page_count_unchanged": after["page_count"]}


def job_id(x: str) -> str:
    return x


@case("T16", "released/expired jobs return 410 during the tombstone window")
def t16(stack: Stack) -> dict:
    out = {}
    # released
    r = stack.create_job(FIX / "csv_utf8.csv")
    jid = r.json()["job_id"]
    final = stack.wait_status(jid, {"completed"}, timeout=60)
    stack.ack(jid, final["result_version"])
    stack.wait_retention(jid, {"released"}, timeout=30)
    g1 = stack.get_result(jid)
    out["released"] = {"download_http": g1.status,
                       "job_http": stack.get_job(jid).status,
                       "retention": stack.get_job(jid).json().get("retention_status")}
    # expired
    r2 = stack.create_job(FIX / "txt_plain.txt", fields={"ttl_seconds": "1"})
    jid2 = r2.json()["job_id"]
    stack.wait_status(jid2, {"completed"}, timeout=60)
    stack.wait_retention(jid2, {"expired"}, timeout=45)
    g2 = stack.get_result(jid2)
    out["expired"] = {"download_http": g2.status,
                      "retention": stack.get_job(jid2).json().get("retention_status")}
    # Cross-tenant access must not reveal existence.
    other = http_request(stack.api, "GET", f"/v1/jobs/{jid}", headers={"Authorization": "Bearer other-owner"})
    out["cross_owner"] = {"http": other.status, "code": ((other.json() or {}).get("error") or {}).get("code")}
    assert out["released"]["download_http"] == 410, out
    assert out["expired"]["download_http"] == 410, out
    assert out["cross_owner"]["http"] == 401, out  # unknown token, no info leak
    return out


@case("T17", "cancel during a parse stops the worker within the heartbeat budget")
def t17(stack: Stack) -> dict:
    r = stack.create_job(FIX / "txt_plain.txt")
    jid = r.json()["job_id"]
    # The kill stack runs a 40 s mock parse, so the job is running when we cancel.
    deadline = time.time() + 30
    running = False
    while time.time() < deadline:
        job = stack.get_job(jid).json()
        if job["status"] == "running":
            running = True
            break
        if job["status"] in ("completed", "failed", "cancelled"):
            break
        time.sleep(0.2)
    assert running, f"job never reached running (last={job['status']})"

    t0 = time.time()
    cancelled = stack.cancel(jid)
    assert cancelled.status == 202, (cancelled.status, cancelled.text()[:200])
    assert cancelled.json()["status"] == "cancelled"
    final = stack.wait_status(jid, {"cancelled"}, timeout=15)
    observed_in = time.time() - t0
    assert observed_in < 12, f"cancellation took {observed_in:.1f}s"
    assert final.get("cancel_requested_at"), "cancel_requested_at not exposed"
    assert not final.get("result_urls"), "a cancelled job must not expose result urls"

    with stack.db() as db:
        states = [row["state"] for row in db.execute(
            "SELECT state FROM attempts WHERE job_id=?", (jid,))]
    assert all(s in ("cancelled", "failed", "expired") for s in states), states

    # Give the worker 3 heartbeats: it must have seen the cancel and stopped, and
    # no result may appear afterwards.
    time.sleep(4)
    after = stack.get_job(jid).json()
    worker_log = stack.logs["worker0"].read_text(errors="replace")
    worker_stopped = jid in worker_log and "cancelled by coordinator" in worker_log
    assert after["status"] == "cancelled" and not after.get("result_urls"), after
    assert worker_stopped, "worker never logged that it stopped on cancellation"

    # The input is cleaned up by the normal retention path.
    released = stack.wait_retention(jid, {"released"}, timeout=60)
    assert not stack.stored_files(jid), "cancelled job left files behind"
    return {"job_id": jid, "cancel_observed_s": round(observed_in, 2),
            "cancel_requested_at": final["cancel_requested_at"],
            "worker_stopped": worker_stopped, "attempt_states": states,
            "retention": released["retention_status"]}


# --------------------------------------------------------------------------- runner
LIFECYCLE_CASES = ["T03", "T04", "T05", "T06", "T07", "T08", "T09", "T10", "T12", "T13", "T14", "T15", "T16", "T17"]
REAL_CASES = ["T01", "T02", "T11"]


def build_stacks(profile: str, engines: str) -> dict[str, Stack]:
    """Stacks are per-case-group so one fault injection cannot poison another."""
    stacks: dict[str, Stack] = {}
    if profile == "lifecycle":
        common = dict(engines="mock", api_count=2, worker_count=1)
        # Long TTL by default; TTL-specific cases override via ttl_seconds.
        mm = {"PARSEDOCK_MOCK_DELAY_MS": "150"}
        stacks["main"] = Stack("main", worker_env=mm,
                               coord_args=["--result-ttl", "120s", "--download-lease", "5s",
                                           "--worker-lease", "5s", "--tombstone-ttl", "20s"],
                               **common)
        stacks["bigstream"] = Stack("bigstream", worker_env={**mm, "PARSEDOCK_MOCK_BLOB_KB": "32768"},
                                    coord_args=["--download-lease", "5s", "--result-ttl", "120s"],
                                    **common)
        stacks["kill"] = Stack("kill", worker_env={**mm, "PARSEDOCK_MOCK_DELAY_MS": "40000"},
                               coord_args=["--worker-lease", "5s", "--result-ttl", "120s"], **common)
    else:
        args = ["--result-ttl", "300s", "--max-parse-minutes", "20"]
        stacks["real"] = Stack("real", engines=engines, api_count=1, worker_count=1,
                               coord_args=args,
                               worker_args=["--max-parse-minutes", "20",
                                            "--xberg-bin", str(ROOT / "runtime/engines/xberg-cli/xberg"),
                                            "--xberg-lib-dir", str(ROOT / "runtime/engines/xberg-cli"),
                                            "--xberg-ocr-lang", "eng",
                                            "--docling-rs-bin", str(ROOT / "runtime/engines/docling-rs/docling-rs"),
                                            "--docling-rs-models", str(ROOT / "runtime/engines/docling-rs"),
                                            "--docling-serve-url", "http://127.0.0.1:15001",
                                            "--docling-serve-gpu", "0",
                                            "--tika-url", "http://127.0.0.1:15002"])
    return stacks


CASE_STACK = {
    "T05": "bigstream",
    "T09": "kill",
    "T17": "kill",
}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--profile", choices=["lifecycle", "real"], default="lifecycle")
    ap.add_argument("--engines", default="native,xberg,docling-rs")
    ap.add_argument("--only", default="")
    ap.add_argument("--keep", action="store_true", help="keep stacks running after the run")
    args = ap.parse_args()

    if not BIN.exists():
        print(f"missing binary {BIN}; run `make build`", file=sys.stderr)
        return 2
    WORK.mkdir(parents=True, exist_ok=True)
    RAW_DIR.mkdir(parents=True, exist_ok=True)
    SUMFILE.parent.mkdir(parents=True, exist_ok=True)

    cases = {f._case[0]: f for f in
             (t01, t02, t03, t04, t05, t06, t07, t08, t09, t10, t11, t12, t13, t14, t15, t16, t17)}
    order = LIFECYCLE_CASES if args.profile == "lifecycle" else REAL_CASES
    if args.only:
        order = [x.strip() for x in args.only.split(",") if x.strip()]

    stacks = build_stacks(args.profile, args.engines)
    results: list[dict] = []
    started = utcnow()

    for cid in order:
        fn = cases[cid]
        cname = fn._case[1]
        stack = stacks[CASE_STACK.get(cid, "main" if args.profile == "lifecycle" else "real")]
        c = Case(id=cid, name=cname)
        print(f"[{cid}] {cname} ... ", end="", flush=True)
        t0 = time.time()
        try:
            if not stack.procs:
                stack.start()
            c.detail = fn(stack) or {}
            c.status = "PASS"
        except Exception as e:
            c.status = "FAIL"
            c.error = f"{type(e).__name__}: {e}"
            c.detail = {"traceback": traceback.format_exc()}
        c.detail["elapsed_s"] = round(time.time() - t0, 2)
        print(f"{c.status} ({c.detail['elapsed_s']}s)" + (f"  {c.error}" if c.error else ""))
        results.append(c.as_dict())

    if not args.keep:
        for s in stacks.values():
            if s.procs:
                s.stop()

    stamp = datetime.now().strftime("%Y%m%dT%H%M%S")
    raw = RAW_DIR / f"acceptance-{args.profile}-{stamp}.json"
    payload = {
        "profile": args.profile,
        "engines": args.engines,
        "started_at": started,
        "finished_at": utcnow(),
        "host": {"cpu_cores": os.cpu_count(), "uname": os.uname().release},
        "binary": str(BIN.relative_to(ROOT)),
        "binary_sha256": sha256(BIN),
        "cases": results,
    }
    raw.write_text(json.dumps(payload, indent=2, ensure_ascii=False), encoding="utf-8")

    summary = {r["id"]: r["status"] for r in results}
    prev = {}
    if SUMFILE.exists():
        try:
            prev = json.loads(SUMFILE.read_text())
        except Exception:
            prev = {}
    prev.setdefault("profiles", {})[args.profile] = {
        "updated_at": utcnow(), "results": summary, "raw": str(raw.relative_to(ROOT)),
    }
    prev["profiles"][args.profile]["engines"] = args.engines
    SUMFILE.write_text(json.dumps(prev, indent=2), encoding="utf-8")

    print("\n=== summary ===")
    for r in results:
        print(f"  {r['id']}  {r['status']:4s}  {r['name']}")
    failed = [r for r in results if r["status"] == "FAIL"]
    print(f"\nraw: {raw.relative_to(ROOT)}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
