#!/usr/bin/env python3
"""Python SDK error matrix and stream-resume tests.

`test_sdk.py` covers the happy path and the ACK contract. This file covers the
paths an application actually hits in production, each against a real standalone
process with the mock engine:

  rate_limited   429 + the client's own retry/backoff
  quota_exceeded 429 with a different code - the client must not mislabel it
  file_too_large 413, non-retryable
  encrypted_file 422 from a password-protected PDF
  gpu_unavailable 409 when no GPU worker is registered
  not_found      404 for an unknown job, non-retryable
  sse resume     Last-Event-ID continues without replaying earlier events

Usage: python sdk/python/test_sdk_matrix.py
Exit code 0 = all assertions passed.
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
FIXTURE = ROOT / "benchmark" / "fixtures" / "generated" / "pdf_text_en_3p.pdf"
ENCRYPTED = ROOT / "benchmark" / "fixtures" / "invalid" / "password.pdf"
TOKEN = "sdk-matrix-token"
OWNER = "sdk-matrix"
PORT, INT_PORT = 18186, 18187

failures: list[str] = []


def free_ports() -> None:
    for p in (PORT, INT_PORT):
        with socket.socket() as s:
            s.settimeout(0.3)
            if s.connect_ex(("127.0.0.1", p)) == 0:
                raise SystemExit(f"port {p} busy; stop the previous run")


class Stack:
    """A standalone instance with the given extra flags."""

    def __init__(self, *extra: str, env: dict[str, str] | None = None) -> None:
        self.data = Path(tempfile.mkdtemp(prefix="sdk-matrix-"))
        e = {**os.environ, "PARSEDOCK_MOCK_DELAY_MS": "400"}
        if env:
            e.update(env)
        self.proc = subprocess.Popen(
            [str(BIN), "--role", "standalone",
             "--listen", f"127.0.0.1:{PORT}",
             "--internal-listen", f"127.0.0.1:{INT_PORT}",
             "--data-dir", str(self.data), "--work-dir", str(self.data / "work"),
             "--engines", "mock", "--internal-token", "itest",
             "--api-keys", f"{TOKEN}:{OWNER}",
             "--result-ttl", "120s", "--cleanup-interval", "1s",
             "--worker-lease", "5s", *extra],
            env=e, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, start_new_session=True)
        self.base = f"http://127.0.0.1:{PORT}"
        for _ in range(100):
            try:
                with urllib.request.urlopen(self.base + "/readyz", timeout=1):
                    return
            except Exception:  # noqa: BLE001
                time.sleep(0.2)
        raise SystemExit("stack did not become ready")

    def stop(self) -> None:
        try:
            os.killpg(os.getpgid(self.proc.pid), signal.SIGTERM)
            self.proc.wait(10)
        except Exception:  # noqa: BLE001
            self.proc.kill()
        shutil.rmtree(self.data, ignore_errors=True)


def expect_error(what: str, fn, status: int, code: str, *, retryable: bool | None = None) -> None:
    try:
        fn()
    except ParseDockError as e:
        if e.status != status or e.code != code:
            failures.append(f"{what}: got {e.status}/{e.code}, want {status}/{code}")
            return
        if retryable is not None and e.retryable != retryable:
            failures.append(f"{what}: retryable={e.retryable}, want {retryable}")
            return
        print(f"[ok] {what}: {e.status} {e.code} (retryable={e.retryable})")
        return
    failures.append(f"{what}: no error raised")


def test_rate_and_size(stack: Stack) -> None:
    with ParseDockClient(stack.base, token=TOKEN, max_retries=0) as c:
        # The burst is 2: the fourth request in the same second must be refused.
        for _ in range(3):
            try:
                c.get_job("job_00000000000000000000000000")
            except ParseDockError:
                pass  # 404 is expected; this only consumes the burst
        expect_error("rate limited", lambda: c.get_job("job_00000000000000000000000000"),
                     429, "rate_limited", retryable=True)
        # With retries enabled the same call succeeds once the window passes.
        with ParseDockClient(stack.base, token=TOKEN, max_retries=3) as rc:
            try:
                rc.create_job(FIXTURE)
                print("[ok] rate limit retried transparently by the client")
            except ParseDockError as e:
                failures.append(f"rate limit retry failed: {e.status}/{e.code}")

        # Size limit is checked before parsing and must not be retried.
        big = Path(tempfile.mkdtemp(prefix="sdk-big-")) / "big.pdf"
        big.write_bytes(FIXTURE.read_bytes() + b"\0" * (2 << 20))
        try:
            expect_error("file too large", lambda: c.create_job(big), 413, "file_too_large",
                         retryable=False)
        finally:
            shutil.rmtree(big.parent, ignore_errors=True)


def test_validation_errors(stack: Stack) -> None:
    with ParseDockClient(stack.base, token=TOKEN) as c:
        expect_error("unknown job", lambda: c.get_job("job_00000000000000000000000000"),
                     404, "not_found", retryable=False)
        if ENCRYPTED.exists():
            expect_error("encrypted pdf", lambda: c.create_job(ENCRYPTED), 422, "encrypted_file",
                         retryable=False)
        else:
            print("[skip] encrypted fixture missing")
        expect_error("gpu required without a gpu worker",
                     lambda: c.create_job(FIXTURE, device="gpu_required"),
                     409, "gpu_unavailable", retryable=False)


def test_quota(stack: Stack) -> None:
    # Retries are ON: the point is that a 429 quota_exceeded is not retried even
    # though rate_limited (same status) is. Counting the attempts proves it.
    class Counting(ParseDockClient):
        attempts = 0

        def _request(self, method, path, body=None, headers=None, *, timeout=None, retry=True):
            type(self).attempts += 1
            return super()._request(method, path, body, headers, timeout=timeout, retry=retry)

    with Counting(stack.base, token=TOKEN, max_retries=3) as c:
        c.create_job(FIXTURE)                       # consumes the daily quota
        before = Counting.attempts
        expect_error("quota", lambda: c.create_job(FIXTURE), 429, "quota_exceeded", retryable=False)
        if Counting.attempts - before != 1:
            failures.append(f"quota was retried {Counting.attempts - before} times")
        else:
            print("[ok] quota_exceeded was attempted exactly once (no retry)")



def test_sse_resume(stack: Stack) -> None:
    with ParseDockClient(stack.base, token=TOKEN) as c:
        job = c.create_job(FIXTURE)
        seen: list[int] = []
        skipped: list[int] = []
        for ev in c.events(job["job_id"]):
            eid = ev.get("event_id")
            if eid is None:
                continue  # keep-alive / comment frames carry no id
            seen.append(eid)
            if len(seen) == 2:
                # Abandon the first stream mid-flight and resume from the last id.
                skipped = seen[:]
                break
        if not skipped:
            failures.append("sse: first stream produced no events")
            return
        resumed: list[int] = []
        for ev in c.events(job["job_id"], last_event_id=skipped[-1]):
            eid = ev.get("event_id")
            if eid is None:
                continue
            resumed.append(eid)
            if ev.get("event_type") == "stream_end":
                break
        if not resumed:
            failures.append("sse resume: no events after Last-Event-ID")
            return
        if min(resumed) <= skipped[-1]:
            failures.append(f"sse resume replayed old events: skipped={skipped} resumed={resumed[:3]}")
            return
        if resumed != sorted(set(resumed)):
            failures.append(f"sse resume out of order or duplicated: {resumed}")
            return
        print(f"[ok] sse resume from id {skipped[-1]}: {len(resumed)} new events, terminal reached")
        c.wait(job["job_id"], timeout=60)


def test_generated_idempotency_key(stack: Stack) -> None:
    """create_job always sends a key, so a retried upload cannot duplicate."""
    import urllib.request as u

    sent: list[tuple[bytes, dict]] = []

    class Recording(ParseDockClient):
        def _request(self, method, path, body=None, headers=None, *, timeout=None, retry=True):
            if method == "POST" and path == "/v1/jobs":
                sent.append((body or b"", dict(headers or {})))
            return super()._request(method, path, body, headers, timeout=timeout, retry=retry)

    with Recording(stack.base, token=TOKEN) as c:
        job = c.create_job(FIXTURE)
    body, headers = sent[0]
    if b'name="idempotency_key"' not in body:
        failures.append("create_job sent no idempotency_key")
        return
    # Replaying the identical request (what a retry does) must return the same job.
    req = u.Request(stack.base + "/v1/jobs", data=body, method="POST",
                    headers={"Authorization": f"Bearer {TOKEN}",
                             "Content-Type": headers.get("Content-Type", "")})
    with u.urlopen(req, timeout=30) as resp:
        replay = json.loads(resp.read())
    if replay.get("job_id") != job["job_id"]:
        failures.append(f"replayed upload created a second job: {job['job_id']} vs {replay.get('job_id')}")
        return
    print("[ok] generated idempotency key is sent and a replay returns the same job")


def test_encoded_file_names(stack: Stack) -> None:
    """File names survive the multipart hop byte for byte (issue #66).

    The client sends non-plain names with an RFC 5987 filename* parameter. A quote
    used to end the Content-Disposition parameter, so the coordinator answered
    ``400 multipart body contains no file part`` even though the bytes were fine.
    """
    names = ["plain name.pdf", "café.pdf", "测试-合同.pdf", 'a"b.pdf', "back\\slash.pdf"]
    with tempfile.TemporaryDirectory(prefix="sdk-names-") as tmp:
        with ParseDockClient(stack.base, token=TOKEN) as c:
            for name in names:
                target = Path(tmp) / name
                shutil.copyfile(FIXTURE, target)
                created = c.create_job(target, mode="fast")
                if created.get("input_name") != name:
                    failures.append(
                        f"input_name for {name!r} became {created.get('input_name')!r}")
                    continue
                done = c.wait(created["job_id"], timeout=60)
                if done["status"] != "completed":
                    failures.append(f"upload named {name!r} did not complete: {done}")
                    continue
                c.ack(done["job_id"], done["result_version"])
                c.wait_released(done["job_id"], timeout=30)
                print(f"[ok] file name {name!r} round-tripped through the upload")


def main() -> int:
    if not BIN.exists():
        raise SystemExit("build the binary first: make build")
    free_ports()

    # One stack with a small burst, a 1 MiB upload cap and no quota.
    stack = Stack("--rate-limit", "2", "--rate-burst", "2", "--max-upload-mb", "1")
    try:
        test_rate_and_size(stack)
    finally:
        stack.stop()

    time.sleep(1.1)  # let the rate-limit window drain before the next stack
    stack = Stack("--rate-limit", "100", "--rate-burst", "50")
    try:
        test_validation_errors(stack)
        test_encoded_file_names(stack)
        test_sse_resume(stack)
        test_generated_idempotency_key(stack)
    finally:
        stack.stop()

    time.sleep(1.1)
    stack = Stack("--rate-limit", "100", "--rate-burst", "50", "--quota-jobs-per-day", "1")
    try:
        test_quota(stack)
    finally:
        stack.stop()

    if failures:
        for f in failures:
            print(f"[FAIL] {f}")
        return 1
    print("\nSDK error matrix: all checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
