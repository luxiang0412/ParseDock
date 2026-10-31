#!/usr/bin/env python3
"""PR-00 runtime contract checks (PR00-02/03/04/05/06/07/09/10/14).

Runs against a real coordinator + API + worker with the deterministic `mock`
engine, plus one stack with the `native` engine for the GB18030 check. Every
check asserts observable HTTP behaviour only.

    python tests/integration/pr00_contract.py

Raw evidence: benchmark/results/raw/pr00-contract-<stamp>.json
"""
from __future__ import annotations

import json
import os
import signal
import sys
import time
import traceback
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from acceptance import (  # noqa: E402
    API_TOKEN, FIX, INTERNAL_TOKEN, ROOT, Stack, http_request, multipart,
)

RAW = ROOT / "benchmark" / "results" / "raw"
results: list[dict] = []


def record(cid: str, name: str, fn):
    started = time.time()
    try:
        detail = fn() or {}
        results.append({"id": cid, "name": name, "status": "PASS", "detail": detail,
                        "elapsed_s": round(time.time() - started, 2)})
        print(f"[{cid}] PASS  {name} ({round(time.time() - started, 2)}s)")
    except Exception as e:
        results.append({"id": cid, "name": name, "status": "FAIL",
                        "detail": {"traceback": traceback.format_exc()},
                        "error": f"{type(e).__name__}: {e}",
                        "elapsed_s": round(time.time() - started, 2)})
        print(f"[{cid}] FAIL  {name}: {e}")


def wait_terminal(stack: Stack, jid: str, timeout: float = 60) -> dict:
    return stack.wait_status(jid, {"completed", "failed", "cancelled"}, timeout=timeout)


def main() -> int:
    mock = Stack(
        "pr00-mock", engines="mock", api_count=2, worker_count=1,
        worker_env={"PARSEDOCK_MOCK_DELAY_MS": "250", "PARSEDOCK_MOCK_BLOB_KB": "32768"},
        api_keys_extra=["rotated-token:itest-owner"],
        coord_args=["--result-ttl", "120s", "--cleanup-interval", "1s",
                    "--download-lease", "4s", "--worker-lease", "6s",
                    "--tombstone-ttl", "30s", "--work-max-age", "2s",
                    "--work-scrub-interval", "1s"],
    )
    native = Stack(
        "pr00-native", engines="native", api_count=1, worker_count=1,
        coord_args=["--result-ttl", "120s", "--cleanup-interval", "1s"],
    )
    # A stack with only CPU engines must reject gpu_required (PR00-14).
    cpu_only = Stack("pr00-cpu", engines="docling-rs", api_count=1, worker_count=1,
                     coord_args=["--result-ttl", "60s"],
                     worker_args=["--docling-rs-bin", str(ROOT / "runtime/engines/docling-rs/docling-rs"),
                                  "--docling-rs-models", str(ROOT / "runtime/engines/docling-rs")])

    try:
        mock.start()
        native.start()

        # ---------------------------------------------------------------- PR00-02
        def pr00_02():
            out = {}
            for mode in ("auto", "fast", "accurate"):
                r = native.create_job(FIX / "csv_gb18030.csv", fields={"mode": mode})
                assert r.status == 202, (mode, r.status, r.text()[:200])
                jid = r.json()["job_id"]
                final = wait_terminal(native, jid)
                assert final["status"] == "completed", (mode, final)
                md = native.get_result(jid, "markdown").text()
                ir = native.get_result(jid, "json").json()
                assert "区域" in md, f"{mode}: GB18030 header lost: {md[:120]!r}"
                assert "ַ" not in md, f"{mode}: mojibake detected"
                out[mode] = {"engine": final["engine"], "has_region": "区域" in md,
                             "rows": len(ir.get("tables", [{}])[0].get("cells", [])) if ir.get("tables") else 0}
            return out

        # ---------------------------------------------------------------- PR00-03
        def pr00_03():
            r = mock.create_job(FIX / "txt_plain.txt")
            jid = r.json()["job_id"]
            final = wait_terminal(mock, jid)
            v = final["result_version"]
            blocked = http_request(mock.api, "POST", f"/v1/jobs/{jid}/cancel", b"",
                                   mock.headers({"Content-Type": "application/json"}))
            assert blocked.status == 409, (blocked.status, blocked.text()[:200])
            assert mock.get_job(jid).json()["retention_status"] == "active"
            # Even with the correct version, cancel must not release a completed
            # result: releasing is ACK's job (single path, no cancel/ACK race).
            refused = http_request(mock.api, "POST", f"/v1/jobs/{jid}/cancel",
                                   json.dumps({"result_version": v}).encode(),
                                   mock.headers({"Content-Type": "application/json"}))
            assert refused.status == 409, (refused.status, refused.text()[:200])
            assert mock.get_job(jid).json()["retention_status"] == "active"
            acked = http_request(mock.api, "POST", f"/v1/jobs/{jid}/ack",
                                 json.dumps({"result_version": v}).encode(),
                                 mock.headers({"Content-Type": "application/json"}))
            assert acked.status == 202, (acked.status, acked.text()[:200])
            mock.wait_retention(jid, {"released"}, timeout=40)

            # queued/running cancel stays a cancellation, not a release
            j2 = mock.create_job(FIX / "txt_plain.txt").json()["job_id"]
            c2 = http_request(mock.api, "POST", f"/v1/jobs/{j2}/cancel", b"",
                              mock.headers({"Content-Type": "application/json"}))
            assert c2.status == 202, c2.status
            assert c2.json()["status"] == "cancelled", c2.json()
            with mock.db() as db:
                states = [r["state"] for r in db.execute(
                    "SELECT state FROM attempts WHERE job_id=?", (j2,))]
            assert all(s in ("cancelled", "failed", "expired") for s in states), states
            return {"completed_cancel_no_version": blocked.status,
                    "completed_cancel_with_version": refused.status,
                    "running_cancel_status": c2.json()["status"], "attempts": states}

        # ---------------------------------------------------------------- PR00-04
        def pr00_04():
            body, ctype = multipart({"mode": "fast"}, [("file", FIX / "txt_plain.txt")])
            sync = http_request(mock.api, "POST", "/v1/parse?timeout_ms=20000", body,
                                mock.headers({"Content-Type": ctype}))
            assert sync.status == 200, (sync.status, sync.text()[:200])
            ir = sync.json()
            assert ir["schema_version"] == "doc-ir/0.1", ir.get("schema_version")
            job_id = sync.headers.get("x-job-id")
            assert job_id, "sync response must expose X-Job-Id"
            # The synchronous body is not proof of receipt: the result stays
            # downloadable until the caller ACKs it (or the TTL expires).
            still = http_request(mock.api, "GET", f"/v1/jobs/{job_id}/result?format=markdown",
                                 headers=mock.headers())
            assert still.status == 200, f"sync result was released without an ACK: {still.status}"
            ack = http_request(mock.api, "POST", f"/v1/jobs/{job_id}/ack",
                               json.dumps({"result_version": sync.headers.get("x-result-version")}).encode(),
                               mock.headers({"Content-Type": "application/json"}))
            assert ack.status == 202, (ack.status, ack.text()[:200])
            gone = http_request(mock.api, "GET", f"/v1/jobs/{job_id}/result?format=markdown",
                                headers=mock.headers())
            assert gone.status == 410, f"result survived its ACK: {gone.status}"

            # timeout → 202, job continues asynchronously and completes normally
            body, ctype = multipart({"mode": "fast"}, [("file", FIX / "txt_plain.txt")])
            t0 = time.time()
            async_resp = http_request(mock.api, "POST", "/v1/parse?timeout_ms=100", body,
                                      mock.headers({"Content-Type": ctype}))
            assert async_resp.status == 202, (async_resp.status, async_resp.text()[:200])
            jid = async_resp.json()["job_id"]
            final = wait_terminal(mock, jid)
            assert final["status"] == "completed", final
            assert time.time() - t0 < 30
            return {"sync_http": sync.status, "sync_result_before_ack": still.status,
                    "sync_result_after_ack": gone.status,
                    "async_http": async_resp.status, "async_final": final["status"]}

        # ---------------------------------------------------------------- PR00-05
        def pr00_05():
            r = mock.create_job(FIX / "txt_plain.txt")
            jid = r.json()["job_id"]
            final = wait_terminal(mock, jid)
            v = final["result_version"]
            # Start a download, then ACK while it is in flight.
            import http.client
            conn = http.client.HTTPConnection("127.0.0.1", mock.api, timeout=120)
            conn.request("GET", f"/v1/jobs/{jid}/result?format=markdown", headers=mock.headers())
            resp = conn.getresponse()
            assert resp.status == 200
            head = resp.read(2048)
            ack = mock.ack(jid, v)
            assert ack.status == 202, ack.status
            second = mock.get_result(jid)
            assert second.status == 410, second.status
            time.sleep(1.5)  # > cleanup interval, < download lease TTL
            assert mock.stored_files(jid), "cleaner deleted files under a live lease"
            rest = resp.read()
            conn.close()
            released = mock.wait_retention(jid, {"released"}, timeout=40)
            assert not mock.stored_files(jid)
            return {"bytes": len(head) + len(rest), "ack": ack.status,
                    "second_download": second.status, "final": released["retention_status"]}

        # ---------------------------------------------------------------- PR00-06
        def pr00_06():
            r = mock.create_job(FIX / "pdf_text_en_3p.pdf")
            jid = r.json()["job_id"]
            wait_terminal(mock, jid)
            etags, versions, bodies = {}, set(), {}
            for fmt in ("markdown", "text", "json"):
                resp = mock.get_result(jid, fmt)
                assert resp.status == 200, (fmt, resp.status)
                etags[fmt] = resp.headers.get("etag")
                versions.add(resp.headers.get("x-result-version"))
                bodies[fmt] = resp.body
            assert len(versions) == 1, f"X-Result-Version must be identical for all formats: {versions}"
            assert len(set(etags.values())) == 3, f"ETags must differ per representation: {etags}"
            # The ETag must be the sha256 of *that* artifact.
            import hashlib
            for fmt, body in bodies.items():
                want = '"sha256:' + hashlib.sha256(body).hexdigest() + '"'
                assert etags[fmt] == want, f"{fmt}: ETag {etags[fmt]} != artifact hash"
            cond = http_request(mock.api, "GET", f"/v1/jobs/{jid}/result?format=markdown",
                                headers=mock.headers({"If-None-Match": etags["markdown"]}))
            assert cond.status == 304, f"If-None-Match not honoured: {cond.status}"
            assert cond.body == b"", "304 must not carry a body"

            # PR-06: gzip on the wire with a weak validator for the encoded form.
            import gzip
            gz = http_request(mock.api, "GET", f"/v1/jobs/{jid}/result?format=markdown",
                              headers=mock.headers({"Accept-Encoding": "gzip"}))
            assert gz.status == 200, gz.status
            assert gz.headers.get("content-encoding") == "gzip", gz.headers
            gz_etag = gz.headers.get("etag", "")
            assert gz_etag.startswith("W/"), f"encoded representation must use a weak ETag: {gz_etag}"
            assert gz.headers.get("x-result-version") == versions.copy().pop(), gz.headers
            assert gzip.decompress(gz.body) == bodies["markdown"], "gzip body does not match the plain artifact"
            gz_cond = http_request(mock.api, "GET", f"/v1/jobs/{jid}/result?format=markdown",
                                   headers=mock.headers({"Accept-Encoding": "gzip", "If-None-Match": gz_etag}))
            assert gz_cond.status == 304, f"weak validator not honoured: {gz_cond.status}"
            return {"etags": etags, "result_version": versions.pop(), "conditional": cond.status,
                    "gzip_etag": gz_etag, "gzip_bytes": len(gz.body), "gzip_conditional": gz_cond.status}

        # ---------------------------------------------------------------- PR00-07
        def pr00_07():
            r = mock.create_job(FIX / "txt_plain.txt")
            jid = r.json()["job_id"]
            wait_terminal(mock, jid)
            proc_events = read_sse(mock, jid, scope="processing", max_events=40)
            assert "stream_end" in proc_events, proc_events
            assert "released" not in proc_events, f"processing scope leaked retention events: {proc_events}"

            v = mock.get_job(jid).json()["result_version"]
            import threading
            holder = {}

            def watch():
                holder["events"] = read_sse(mock, jid, scope="retention", max_events=80, timeout=40)

            t = threading.Thread(target=watch)
            t.start()
            time.sleep(0.5)
            mock.ack(jid, v)
            t.join(timeout=45)
            evs = holder.get("events") or []
            assert "release_pending" in evs, evs
            assert "released" in evs, evs
            assert "stream_end" in evs, evs

            reset = read_sse(mock, jid, scope="processing", last_event_id=1, max_events=10)
            assert "cursor_reset" in reset or "snapshot" in reset, reset
            return {"processing_events": proc_events, "retention_events": evs,
                    "reset_events": reset}

        # ---------------------------------------------------------------- PR00-09
        def pr00_09():
            out = {}
            for label, fields_first in (("file_first", True), ("fields_first", False)):
                if fields_first:
                    order = [("file", FIX / "csv_utf8.csv")]
                    form = {"mode": "accurate", "device": "cpu", "ttl_seconds": "90"}
                else:
                    order = [("file", FIX / "csv_utf8.csv")]
                    form = {"mode": "accurate", "device": "cpu", "ttl_seconds": "90"}
                # Build the body manually so the part order is explicit.
                boundary = "----pr00" + label
                body = bytearray()
                if not fields_first:
                    for k, v in form.items():
                        body += f"--{boundary}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n".encode()
                for field, path in order:
                    body += (f"--{boundary}\r\nContent-Disposition: form-data; name=\"{field}\"; "
                             f"filename=\"{path.name}\"\r\nContent-Type: application/octet-stream\r\n\r\n").encode()
                    body += path.read_bytes() + b"\r\n"
                if fields_first:
                    for k, v in form.items():
                        body += f"--{boundary}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n".encode()
                body += f"--{boundary}--\r\n".encode()
                resp = http_request(mock.api, "POST", "/v1/jobs", bytes(body),
                                    mock.headers({"Content-Type": f"multipart/form-data; boundary={boundary}"}))
                assert resp.status == 202, (label, resp.status, resp.text()[:200])
                job = resp.json()
                assert job["requested_mode"] == "accurate", (label, job["requested_mode"])
                assert job["requested_device"] == "cpu", (label, job["requested_device"])
                out[label] = {"status": resp.status, "mode": job["requested_mode"],
                              "device": job["requested_device"]}
                wait_terminal(mock, job["job_id"])
            return out

        # ---------------------------------------------------------------- PR00-10
        def pr00_10():
            # A second token mapped to the same owner (rotation) can read/release.
            r = mock.create_job(FIX / "txt_plain.txt")
            jid = r.json()["job_id"]
            final = wait_terminal(mock, jid)
            rotated = http_request(mock.api, "GET", f"/v1/jobs/{jid}",
                                   headers={"Authorization": "Bearer rotated-token"})
            assert rotated.status == 200, (rotated.status, rotated.text()[:200])
            ack = http_request(mock.api, "POST", f"/v1/jobs/{jid}/ack",
                               json.dumps({"result_version": final["result_version"]}).encode(),
                               {"Authorization": "Bearer rotated-token",
                                "Content-Type": "application/json"})
            assert ack.status == 202, (ack.status, ack.text()[:200])

            # A forged identity header through the API must be ignored.
            other = http_request(mock.api, "GET", f"/v1/jobs/{jid}",
                                 headers={"Authorization": "Bearer not-a-key",
                                          "X-Parsedock-Owner": "someone-else"})
            assert other.status == 401, (other.status, other.text()[:200])
            forged = http_request(mock.api, "GET", "/v1/jobs/does-not-exist",
                                  headers=mock.headers({"X-Parsedock-Owner": "someone-else",
                                                        "X-Internal-Token": INTERNAL_TOKEN}))
            assert forged.status == 404, (forged.status, forged.text()[:200])
            # /internal must not be reachable through the public gateway.
            leak = http_request(mock.api, "GET", "/internal/diagnostics/workers",
                                headers=mock.headers())
            assert leak.status == 404, (leak.status, leak.text()[:200])
            return {"rotated_read": rotated.status, "rotated_ack": ack.status,
                    "bad_token": other.status, "forged_owner": forged.status,
                    "internal_via_api": leak.status}

        # ---------------------------------------------------------------- PR00-12
        def pr00_12():
            """Crash leftovers are swept by the next worker that shares the work dir."""
            work = mock.data_dir / "work"
            stale_job, stale_attempt = "job_stale", "a1"
            stale = work / stale_job / stale_attempt
            stale.mkdir(parents=True, exist_ok=True)
            (stale / "input.pdf").write_bytes(b"x")
            old = time.time() - 3600
            os.utime(stale, (old, old))

            fresh = work / "job_live" / "a1"
            fresh.mkdir(parents=True, exist_ok=True)
            (fresh / "input.pdf").write_bytes(b"y")

            # A second worker on the same work dir sweeps at startup and periodically.
            proc = mock._spawn("worker-scrub", [
                "--role", "worker",
                "--coordinator-url", f"http://127.0.0.1:{mock.port_coord}",
                "--internal-token", INTERNAL_TOKEN,
                "--engines", "mock",
                "--worker-id", "scrub-worker",
                "--work-dir", str(work),
                "--work-max-age", "2s",
                "--work-scrub-interval", "1s",
                "--heartbeat", "2s",
            ], env={"PARSEDOCK_MOCK_DELAY_MS": "50"})
            deadline = time.time() + 20
            while time.time() < deadline and stale.exists():
                time.sleep(0.5)
            killed = kill_probe(proc)
            assert not stale.exists(), "stale attempt directory was not reclaimed"
            assert fresh.exists(), "a recent attempt directory was removed"
            return {"stale_removed": True, "fresh_kept": True, "worker_stopped": killed}

        # ---------------------------------------------------------------- PR00-14
        def pr00_14():
            cpu_only.start()
            r = cpu_only.create_job(FIX / "pdf_text_en_3p.pdf", fields={"device": "gpu_required"})
            body = r.json() or {}
            code = (body.get("error") or {}).get("code")
            assert r.status == 409 and code == "gpu_unavailable", (r.status, body)
            with cpu_only.db() as db:
                n = db.execute("SELECT COUNT(*) c FROM jobs WHERE requested_device='gpu_required'").fetchone()["c"]
            assert n == 0, "rejected gpu_required request persisted a job"
            cpu_only.stop()
            return {"http": r.status, "code": code, "gpu_jobs_persisted": n}

        for cid, name, fn in [
            ("PR00-02", "TXT/CSV/GB18030 decode correctly in auto|fast|accurate", pr00_02),
            ("PR00-03", "cancel never releases a completed result (only ACK does); running cancels", pr00_03),
            ("PR00-04", "sync parse keeps the result until ACK; timeout converts to async", pr00_04),
            ("PR00-05", "ACK vs in-flight download: lease protects the stream", pr00_05),
            ("PR00-06", "per-representation ETag, shared X-Result-Version, 304", pr00_06),
            ("PR00-07", "SSE scope, retention events, cursor reset", pr00_07),
            ("PR00-09", "multipart parts in any order, fields honoured", pr00_09),
            ("PR00-10", "key rotation keeps the owner; forged headers ignored", pr00_10),
            ("PR00-12", "worker crash leftovers are swept from the scratch dir", pr00_12),
            ("PR00-14", "gpu_required without a GPU worker is rejected", pr00_14),
        ]:
            record(cid, name, fn)
    finally:
        for st in (mock, native, cpu_only):
            try:
                if st.procs:
                    st.stop()
            except Exception:
                pass

    RAW.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%dT%H%M%S")
    out = RAW / f"pr00-contract-{stamp}.json"
    out.write_text(json.dumps({"finished_at": datetime.now(timezone.utc).isoformat(),
                               "checks": results}, indent=2), encoding="utf-8")
    failed = [r for r in results if r["status"] == "FAIL"]
    print(f"\n{len(results) - len(failed)}/{len(results)} checks passed — raw: {out.relative_to(ROOT)}")
    return 1 if failed else 0


def kill_probe(proc) -> bool:
    try:
        os.killpg(os.getpgid(proc.pid), signal.SIGTERM)
        proc.wait(timeout=10)
        return True
    except Exception:
        try:
            proc.kill()
        except Exception:
            pass
        return False


def read_sse(stack: Stack, jid: str, scope: str, max_events: int = 40,
             timeout: float = 20, last_event_id: int | None = None) -> list[str]:
    import http.client
    headers = stack.headers()
    if last_event_id is not None:
        headers["Last-Event-ID"] = str(last_event_id)
    conn = http.client.HTTPConnection("127.0.0.1", stack.api, timeout=timeout)
    conn.request("GET", f"/v1/jobs/{jid}/events?scope={scope}", headers=headers)
    resp = conn.getresponse()
    assert resp.status == 200, resp.status
    events: list[str] = []
    buf = b""
    deadline = time.time() + timeout
    while time.time() < deadline and len(events) < max_events:
        chunk = resp.read(1)
        if not chunk:
            break
        buf += chunk
        while b"\n" in buf:
            line, buf = buf.split(b"\n", 1)
            if line.startswith(b"event:"):
                name = line[6:].strip().decode()
                events.append(name)
                if name == "stream_end":
                    conn.close()
                    return events
    conn.close()
    return events


if __name__ == "__main__":
    # The stack needs two owner keys mapping to the same owner for PR00-10.
    os.environ.setdefault("PARSEDOCK_TEST_ROTATION", "1")
    sys.exit(main())
