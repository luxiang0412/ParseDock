#!/usr/bin/env python3
"""End-to-end test of the Python SDK against a live ParseDock stack.

Starts a standalone instance with the mock engine (deterministic, fast), then
exercises the lifecycle exactly as an application would:

  create → wait → download (json/markdown) → ack → 410 → released
  plus: a failing sink must NOT release the result.

Usage: python sdk/python/test_sdk.py
Exit code 0 = all assertions passed.
"""
from __future__ import annotations

import os
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "sdk" / "python"))

from parsedock_sdk import ParseDockClient, ParseDockError  # noqa: E402

BIN = ROOT / "runtime" / "bin" / "docparse"
FIXTURE = ROOT / "benchmark" / "fixtures" / "generated" / "pdf_text_en_3p.pdf"
API_PORT, INT_PORT = 18180, 18181
TOKEN = "sdk-test-token"


def free(port: int) -> None:
    with socket.socket() as s:
        s.settimeout(0.3)
        if s.connect_ex(("127.0.0.1", port)) == 0:
            raise SystemExit(f"port {port} busy; stop the previous run")


def test_cli(stack_api: str, token: str, sample: Path) -> None:
    """The console script must parse a file end to end and print the bytes."""
    out = subprocess.run(
        [sys.executable, "-m", "parsedock_sdk.cli", "parse", str(sample), "--format", "markdown",
         "--api", stack_api, "--token", token],
        capture_output=True)
    assert out.returncode == 0, out.stderr.decode()
    assert out.stdout.strip(), "CLI printed nothing"
    # --keep leaves the result downloadable; the default ACKs it.
    out = subprocess.run(
        [sys.executable, "-m", "parsedock_sdk.cli", "parse", str(sample), "--api", stack_api,
         "--token", token, "--keep", "-o", os.devnull],
        capture_output=True)
    assert out.returncode == 0, out.stderr.decode()


def main() -> int:
    if not BIN.exists():
        raise SystemExit("build the binary first: make build")
    free(API_PORT)
    free(INT_PORT)
    data = ROOT / "runtime" / "sdk-test-data"
    subprocess.run(["rm", "-rf", str(data)], check=False)
    env = {**os.environ, "PARSEDOCK_MOCK_DELAY_MS": "300"}
    proc = subprocess.Popen(
        [str(BIN), "--role", "standalone",
         "--listen", f"127.0.0.1:{API_PORT}",
         "--internal-listen", f"127.0.0.1:{INT_PORT}",
         "--data-dir", str(data), "--work-dir", str(data / "work"),
         "--engines", "mock", "--internal-token", "itest",
         "--api-keys", f"{TOKEN}:sdk-owner",
         "--result-ttl", "120s", "--cleanup-interval", "1s",
         "--download-lease", "5s", "--worker-lease", "5s"],
        env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, start_new_session=True,
    )
    failures = []
    try:
        base = f"http://127.0.0.1:{API_PORT}"
        for _ in range(80):
            try:
                with urllib.request.urlopen(base + "/readyz", timeout=1):
                    break
            except Exception:
                time.sleep(0.2)
        else:
            raise SystemExit("stack did not become ready")

        with ParseDockClient(base, token=TOKEN) as c:
            # 1. create + wait
            job = c.create_job(FIXTURE, mode="fast", idempotency_key="sdk-1")
            assert job["status"] == "queued", job
            replay = c.create_job(FIXTURE, mode="fast", idempotency_key="sdk-1")
            assert replay["job_id"] == job["job_id"], "idempotency key did not replay"
            final = c.wait(job["job_id"], timeout=60)
            assert final["status"] == "completed", final
            print(f"[ok] job {job['job_id']} completed via {final['engine']}")

            # 2. events stream (snapshot + terminal event)
            events = []
            for ev in c.events(job["job_id"]):
                events.append(ev.get("event_type"))
                if ev.get("event_type") == "stream_end":
                    break
            print(f"[ok] sse events: {events}")

            # 3. download both representations
            md, version_md = c.download(job["job_id"], fmt="markdown")
            ir, version_ir = c.download(job["job_id"], fmt="json")
            assert version_md == version_ir == final["result_version"], (version_md, version_ir)
            assert b"Mock parse" in md
            print(f"[ok] downloaded {len(md)} bytes markdown, {len(ir)} bytes json, version {version_md[:20]}…")

            # 4. ACK with a wrong version must be refused
            try:
                c.ack(job["job_id"], "sha256:deadbeef")
                failures.append("stale result_version was accepted")
            except ParseDockError as e:
                assert e.status == 409, e
                print("[ok] stale result_version rejected with 409")

            # 5. failing sink must NOT release
            def boom(_: bytes) -> None:
                raise RuntimeError("disk full")

            try:
                c.download_and_ack(job["job_id"], sink=boom)
                failures.append("download_and_ack swallowed a sink failure")
            except RuntimeError:
                pass
            still = c.get_job(job["job_id"])
            assert still["retention_status"] == "active", still
            print("[ok] failing sink left the result available for retry")

            # 6. successful download_and_ack releases
            payload = c.download_and_ack(job["job_id"], fmt="text")
            assert b"Mock parse" in payload
            expect_gone = False
            try:
                c.download(job["job_id"])
            except ParseDockError as e:
                expect_gone = e.status == 410
            assert expect_gone, "download after ACK did not return 410"
            released = c.wait_released(job["job_id"], timeout=30)
            assert released["retention_status"] == "released", released
            print("[ok] ACK → 410 → released")

            # 7. repeat ACK is idempotent
            again = c.ack(job["job_id"], version_md)
            assert again["retention_status"] in ("released", "release_pending"), again
            print("[ok] repeat ACK idempotent")

            # 8. cancel path. A queued/running job is cancelled; if the mock
            # worker already finished it, cancel must refuse and point at ACK.
            job2 = c.create_job(FIXTURE)
            try:
                res = c.cancel(job2["job_id"])
                assert res["status"] in ("cancelled", "completed"), res
                print(f"[ok] cancel accepted ({res['status']})")
            except ParseDockError as e:
                assert e.status == 409, e
                done2 = c.wait(job2["job_id"], timeout=60)
                released = c.cancel(job2["job_id"], result_version=done2["result_version"])
                assert released["retention_status"] in ("release_pending", "released"), released
                print("[ok] cancel on a completed job needs result_version (409), then releases")

            # 9. sync parse returns the IR
            ir = c.parse_sync(FIXTURE, mode="fast")
            assert ir.get("schema_version") == "doc-ir/0.1", ir
            print("[ok] synchronous parse returned doc-ir/0.1")

            # 10. CLI (console script / python -m) parses and optionally keeps the result.
            test_cli(base, TOKEN, FIXTURE)
            print("[ok] CLI parse wrote output and exited 0")
    except AssertionError as e:
        failures.append(f"assertion: {e}")
    finally:
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        except Exception:
            pass
        try:
            proc.wait(timeout=10)
        except Exception:
            proc.kill()

    if failures:
        for f in failures:
            print(f"[FAIL] {f}")
        return 1
    print("\nSDK lifecycle: all checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
