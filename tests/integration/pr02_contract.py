#!/usr/bin/env python3
"""PR-02 runtime checks: key hashing/rotation/scopes, quotas, audit, headers.

    python tests/integration/pr02_contract.py

Uses a real stack with `--api-keys-file` (no plaintext in argv), three scoped
keys for the same owner, an extra owner, and a concurrency quota of 2.
"""
from __future__ import annotations

import json
import sys
import time
import traceback
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from acceptance import (  # noqa: E402
    API_TOKEN, FIX, OWNER, ROOT, Stack, http_request,
)

RAW = ROOT / "benchmark" / "results" / "raw"
results: list[dict] = []

READ_TOKEN = "read-only-token"
WRITE_TOKEN = "write-only-token"
ACK_TOKEN = "ack-only-token"
OTHER_TOKEN = "other-owner-token"
import hashlib
HASHED_TOKEN = "hashed-token-abc"
HASHED_HEX = hashlib.sha256(HASHED_TOKEN.encode()).hexdigest()
SENTINEL = "SENTINEL-PR02-DOCUMENT-TEXT-9f13"


def record(cid: str, name: str, fn) -> None:
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


def hdr(token: str, extra: dict | None = None) -> dict:
    h = {"Authorization": f"Bearer {token}"}
    if extra:
        h.update(extra)
    return h


def upload(port: int, token: str, path: Path, extra: dict | None = None):
    from acceptance import multipart
    body, ctype = multipart({}, [("file", path)])
    return http_request(port, "POST", "/v1/jobs", body, hdr(token, {"Content-Type": ctype, **(extra or {})}))


def main() -> int:
    stack = Stack(
        "pr02", engines="mock", api_count=1, worker_count=1,
        worker_env={"PARSEDOCK_MOCK_DELAY_MS": "300"},
        keys_file=True,
        api_keys_extra=[
            f"{READ_TOKEN}:{OWNER}:jobs:read",
            f"{WRITE_TOKEN}:{OWNER}:jobs:write",
            f"{ACK_TOKEN}:{OWNER}:jobs:ack",
            f"sha256:{HASHED_HEX}:{OWNER}",
            f"{OTHER_TOKEN}:other-owner:jobs:write+jobs:read+jobs:ack",
        ],
        coord_args=["--result-ttl", "120s", "--cleanup-interval", "1s"],
    )
    stack.start()
    try:
        def pr02_01_scopes():
            w = upload(stack.api, WRITE_TOKEN, FIX / "txt_plain.txt")
            assert w.status == 202, f"write-only upload: {w.status} {w.text()[:200]}"
            jid = w.json()["job_id"]
            read_denied = http_request(stack.api, "GET", f"/v1/jobs/{jid}", headers=hdr(WRITE_TOKEN))
            assert read_denied.status == 403, f"write-only read: {read_denied.status}"
            assert (read_denied.json() or {}).get("error", {}).get("code") == "insufficient_scope"
            ack_denied = http_request(stack.api, "POST", f"/v1/jobs/{jid}/ack",
                                      json.dumps({"result_version": "x"}).encode(),
                                      hdr(WRITE_TOKEN, {"Content-Type": "application/json"}))
            assert ack_denied.status == 403, f"write-only ack: {ack_denied.status}"

            full = upload(stack.api, API_TOKEN, FIX / "txt_plain.txt")
            assert full.status == 202
            denied = http_request(stack.api, "GET", f"/v1/jobs/{full.json()['job_id']}", headers=hdr(READ_TOKEN))
            assert denied.status == 200, f"read-only read: {denied.status}"
            denied = upload(stack.api, READ_TOKEN, FIX / "txt_plain.txt")
            assert denied.status == 403, f"read-only upload: {denied.status}"
            return {"write_only_upload": w.status, "write_only_read": read_denied.status,
                    "write_only_ack": ack_denied.status, "read_only_read": 200,
                    "read_only_upload": denied.status}

        def pr02_02_hashed_key_and_revocation():
            r = upload(stack.api, HASHED_TOKEN, FIX / "csv_utf8.csv")
            assert r.status == 202, f"hashed key upload: {r.status} {r.text()[:200]}"
            unknown = http_request(stack.api, "GET", "/v1/jobs/x", headers=hdr("not-a-key"))
            assert unknown.status == 401
            assert unknown.headers.get("www-authenticate", "").startswith("Bearer")
            # A key that is not in the file at all is the "revoked" case.
            return {"hashed_key_upload": r.status, "unknown_key": unknown.status}

        def pr02_03_headers_and_request_id():
            r = http_request(stack.api, "GET", "/healthz")
            assert r.headers.get("x-content-type-options") == "nosniff", r.headers
            valid = http_request(stack.api, "GET", "/v1/jobs/none", headers=hdr(API_TOKEN, {"X-Request-Id": "req_abc-123"}))
            assert valid.headers.get("x-request-id") == "req_abc-123", valid.headers.get("x-request-id")
            assert valid.status == 404
            bad = http_request(stack.api, "GET", "/v1/jobs/none",
                               headers=hdr(API_TOKEN, {"X-Request-Id": "bad id with spaces"}))
            got = bad.headers.get("x-request-id", "")
            assert got != "bad id with spaces" and got.startswith("req_"), f"invalid id not replaced: {got!r}"
            return {"nosniff": r.headers.get("x-content-type-options"),
                    "echoed_request_id": valid.headers.get("x-request-id"),
                    "generated_request_id": got}

        def pr02_04_audit_log():
            marker = SENTINEL
            path = stack.data_dir / f"{marker}.txt"
            path.write_text(marker + "\nsecond line\n", encoding="utf-8")
            r = upload(stack.api, API_TOKEN, path, {"X-Request-Id": "req_audit-1"})
            assert r.status == 202, r.status
            jid = r.json()["job_id"]
            stack.wait_status(jid, {"completed"}, timeout=60)
            ver = stack.get_job(jid).json()["result_version"]
            assert stack.ack(jid, ver).status == 202
            stack.wait_retention(jid, {"released"}, timeout=40)
            log = stack.logs["coordinator"].read_text(errors="replace")
            lines = []
            for line in log.splitlines():
                try:
                    obj = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if obj.get("audit") is True:
                    lines.append(obj)
            events = [o["event"] for o in lines]
            assert "job_created" in events and "job_acked" in events and "job_released" in events, events
            created = [o for o in lines if o["event"] == "job_created" and o["job_id"] == jid][0]
            assert created["owner_id"] == OWNER
            assert created["request_id"] == "req_audit-1", created.get("request_id")
            joined = json.dumps(lines)
            assert marker not in joined, "audit log leaked document content"
            return {"audit_events": events, "created": {k: created[k] for k in
                    ("owner_id", "job_id", "request_id", "bytes", "format")}}

        def pr02_05_quota_and_isolation():
            quota_stack = Stack(
                "pr02-quota", engines="mock", api_count=1, worker_count=1,
                worker_env={"PARSEDOCK_MOCK_DELAY_MS": "300"},
                keys_file=True,
                api_keys_extra=[f"{OTHER_TOKEN}:other-owner"],
                coord_args=["--result-ttl", "120s", "--cleanup-interval", "1s",
                            "--quota-concurrent", "2"],
            )
            quota_stack.start()
            try:
                return _quota_case(quota_stack)
            finally:
                quota_stack.stop()

        def _quota_case(quota_stack: Stack):
            jids = []
            for _ in range(2):
                r = upload(quota_stack.api, OTHER_TOKEN, FIX / "txt_plain.txt")
                assert r.status == 202, f"quota fill: {r.status} {r.text()[:200]}"
                jids.append(r.json()["job_id"])
            blocked = upload(quota_stack.api, OTHER_TOKEN, FIX / "txt_plain.txt")
            code = ((blocked.json() or {}).get("error") or {}).get("code")
            retry_after = blocked.headers.get("retry-after")
            assert blocked.status == 429 and code == "quota_exceeded", (blocked.status, code)
            assert retry_after and int(retry_after) > 0, retry_after
            # A different owner is unaffected.
            other = upload(quota_stack.api, API_TOKEN, FIX / "txt_plain.txt")
            assert other.status == 202, f"quota leaked across owners: {other.status}"
            # Completing + releasing frees the slot. The jobs belong to
            # other-owner, so they must be polled with that key (cross-owner reads
            # are 404 by design).
            def status_of(jid: str) -> dict:
                r = http_request(quota_stack.api, "GET", f"/v1/jobs/{jid}", headers=hdr(OTHER_TOKEN))
                assert r.status == 200, (r.status, r.text()[:160])
                return r.json()

            for jid in jids:
                deadline = time.time() + 60
                while time.time() < deadline and status_of(jid)["status"] not in ("completed", "failed"):
                    time.sleep(0.3)
                job = status_of(jid)
                assert job["status"] == "completed", job
                ack = http_request(quota_stack.api, "POST", f"/v1/jobs/{jid}/ack",
                                   json.dumps({"result_version": job["result_version"]}).encode(),
                                   hdr(OTHER_TOKEN, {"Content-Type": "application/json"}))
                assert ack.status == 202, (ack.status, ack.text()[:160])
            deadline = time.time() + 40
            while time.time() < deadline:
                if status_of(jids[0])["retention_status"] == "released":
                    break
                time.sleep(0.5)
            again = upload(quota_stack.api, OTHER_TOKEN, FIX / "txt_plain.txt")
            assert again.status == 202, f"quota not released: {again.status} {again.text()[:200]}"
            return {"blocked_http": blocked.status, "code": code, "retry_after": retry_after,
                    "other_owner": other.status, "after_release": again.status}

        def pr02_06_rotation_with_scopes():
            # Two full keys for the same owner: create with one, read+ack with the other.
            r = upload(stack.api, API_TOKEN, FIX / "csv_utf8.csv")
            jid = r.json()["job_id"]
            final = stack.wait_status(jid, {"completed"}, timeout=60)
            assert http_request(stack.api, "GET", f"/v1/jobs/{jid}", headers=hdr(READ_TOKEN)).status == 200
            ack = http_request(stack.api, "POST", f"/v1/jobs/{jid}/ack",
                               json.dumps({"result_version": final["result_version"]}).encode(),
                               hdr(ACK_TOKEN, {"Content-Type": "application/json"}))
            assert ack.status == 202, f"ack-only key ack: {ack.status} {ack.text()[:200]}"
            return {"created_with": "full-key", "read_with": "read-only", "ack_with": "ack-only"}

        for cid, name, fn in [
            ("PR02-01", "scoped keys: write/read/ack separation enforced", pr02_01_scopes),
            ("PR02-02", "hashed key form works; unknown/revoked key is 401", pr02_02_hashed_key_and_revocation),
            ("PR02-03", "nosniff + request-id echo/validation", pr02_03_headers_and_request_id),
            ("PR02-04", "audit lines: identity + decisions, no content", pr02_04_audit_log),
            ("PR02-05", "per-owner concurrency quota with Retry-After", pr02_05_quota_and_isolation),
            ("PR02-06", "rotation: create/read/ack across keys with scopes", pr02_06_rotation_with_scopes),
        ]:
            record(cid, name, fn)
    finally:
        stack.stop()

    RAW.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now().strftime("%Y%m%dT%H%M%S")
    out = RAW / f"pr02-contract-{stamp}.json"
    out.write_text(json.dumps({"finished_at": datetime.now(timezone.utc).isoformat(),
                               "checks": results}, indent=2), encoding="utf-8")
    failed = [r for r in results if r["status"] == "FAIL"]
    print(f"\n{len(results)-len(failed)}/{len(results)} checks passed — raw: {out.relative_to(ROOT)}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
