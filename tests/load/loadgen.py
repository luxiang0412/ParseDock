#!/usr/bin/env python3
"""Load generator for the ParseDock API (PR-07).

Drives N jobs through the public API with a fixed concurrency, then reports
client-side latency percentiles and throughput. The stack must already be running
(see scripts/cluster-lite-load.sh); the generator only uses the HTTP API, so the
same command measures a single host or a real cluster-lite deployment.

    python3 tests/load/loadgen.py --api http://127.0.0.1:18090 --token KEY \
        --file sample.txt --jobs 200 --concurrency 16 --timeout 120

Exit code is non-zero when any job fails, so it can gate CI.
"""
import argparse
import json
import os
import statistics
import sys
import threading
import time
import urllib.error
import urllib.request

BOUNDARY = "----parsedock-load"


def multipart(name: str, content: bytes, fields: dict) -> tuple[bytes, str]:
    out = bytearray()
    for k, v in fields.items():
        out += (f"--{BOUNDARY}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n").encode()
    out += (f"--{BOUNDARY}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{name}\"\r\n"
            "Content-Type: application/octet-stream\r\n\r\n").encode()
    out += content + f"\r\n--{BOUNDARY}--\r\n".encode()
    return bytes(out), f"multipart/form-data; boundary={BOUNDARY}"


def request(url, method="GET", body=None, headers=None, timeout=60):
    req = urllib.request.Request(url, method=method, data=body, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def one(api, token, name, content, mode, device, timeout):
    """Submit one job and wait for a terminal state. Returns (ok, seconds, detail)."""
    start = time.time()
    body, ctype = multipart(name, content, {"mode": mode, "device": device})
    hdrs = {"Authorization": f"Bearer {token}", "Content-Type": ctype}
    code, resp = request(api + "/v1/jobs", "POST", body, hdrs, timeout)
    if code not in (200, 202):
        return False, time.time() - start, f"create {code}: {resp[:120]!r}"
    job = json.loads(resp)["job_id"]
    deadline = start + timeout
    while time.time() < deadline:
        code, resp = request(f"{api}/v1/jobs/{job}", headers={"Authorization": f"Bearer {token}"}, timeout=30)
        if code != 200:
            return False, time.time() - start, f"status {code}"
        st = json.loads(resp)
        if st.get("processing_status") in ("completed", "failed", "cancelled", "expired"):
            ok = st.get("processing_status") == "completed"
            return ok, time.time() - start, st.get("error_code") or st.get("processing_status")
        time.sleep(0.2)
    return False, time.time() - start, "timeout"


def pct(values, p):
    if not values:
        return 0.0
    values = sorted(values)
    k = min(len(values) - 1, max(0, int(round((p / 100.0) * (len(values) - 1)))))
    return values[k]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--api", default="http://127.0.0.1:18090")
    ap.add_argument("--token", default=os.environ.get("PARSEDOCK_TOKEN", "dev-key"))
    ap.add_argument("--file", required=True, help="sample document sent with every job")
    ap.add_argument("--jobs", type=int, default=100)
    ap.add_argument("--concurrency", type=int, default=8)
    ap.add_argument("--mode", default="auto")
    ap.add_argument("--device", default="auto")
    ap.add_argument("--timeout", type=float, default=120)
    ap.add_argument("--out", help="write the JSON report here")
    args = ap.parse_args()

    content = open(args.file, "rb").read()
    name = os.path.basename(args.file)
    results, lock = [], threading.Lock()
    queue = list(range(args.jobs))

    def worker():
        while True:
            with lock:
                if not queue:
                    return
                queue.pop()
            r = one(args.api, args.token, name, content, args.mode, args.device, args.timeout)
            with lock:
                results.append(r)

    started = time.time()
    threads = [threading.Thread(target=worker) for _ in range(max(1, args.concurrency))]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    wall = time.time() - started

    lat = [r[1] for r in results]
    ok = [r for r in results if r[0]]
    failures = {}
    for good, _, detail in results:
        if not good:
            failures[detail] = failures.get(detail, 0) + 1
    report = {
        "jobs": len(results),
        "completed": len(ok),
        "failed": len(results) - len(ok),
        "wall_seconds": round(wall, 2),
        "throughput_jobs_per_s": round(len(results) / wall, 2) if wall else 0,
        "latency_s": {
            "p50": round(pct(lat, 50), 3),
            "p95": round(pct(lat, 95), 3),
            "p99": round(pct(lat, 99), 3),
            "max": round(max(lat), 3) if lat else 0,
            "mean": round(statistics.fmean(lat), 3) if lat else 0,
        },
        "failures": failures,
        "file": name,
        "file_bytes": len(content),
        "concurrency": args.concurrency,
        "mode": args.mode,
        "device": args.device,
    }
    print(json.dumps(report, indent=2, sort_keys=True))
    if args.out:
        with open(args.out, "w") as fh:
            json.dump(report, fh, indent=2, sort_keys=True)
    return 0 if report["failed"] == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
