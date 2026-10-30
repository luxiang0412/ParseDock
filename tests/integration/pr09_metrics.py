#!/usr/bin/env python3
"""PR-09: Prometheus exposition, throttling visibility (RC-5) and no content leakage.

  PR09-01 /internal/metrics requires the internal token
  PR09-02 job lifecycle, queue, disk, worker and retention series are present
  PR09-03 parse duration / result bytes are recorded per engine
  PR09-04 a rate-limited request increments parsedock_throttled_total
  PR09-05 the exposition contains no document content, file name or owner identity
"""
import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BIN = os.environ.get("DOCPARSE_BIN", os.path.join(ROOT, "runtime", "bin", "docparse"))
TOKEN = "pr09-internal-token"
KEY = "pr09-key"
OWNER = "owner-secret-ish"
SENTINEL = "PR09-DOCUMENT-CONTENT-SENTINEL"


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http(port, path, method="GET", data=None, headers=None, timeout=30):
    req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", method=method,
                                 data=data, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def multipart(name, content):
    b = "----pr09"
    body = (f"--{b}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{name}\"\r\n"
            "Content-Type: text/plain\r\n\r\n").encode() + content + f"\r\n--{b}--\r\n".encode()
    return body, {"Content-Type": f"multipart/form-data; boundary={b}"}


def wait_port(port, timeout=25):
    end = time.time() + timeout
    while time.time() < end:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.5):
                return True
        except OSError:
            time.sleep(0.1)
    return False


def main():
    if not os.path.exists(BIN):
        print(f"missing binary {BIN}; run `make build`", file=sys.stderr)
        return 1
    port, cport = free_port(), free_port()
    data = tempfile.mkdtemp(prefix="pr09-")
    log = open(os.path.join(data, "coord.log"), "w")
    p = subprocess.Popen([BIN, "--role", "standalone", "--listen", f"127.0.0.1:{port}",
                          "--internal-listen", f"127.0.0.1:{cport}", "--internal-token", TOKEN,
                          "--data-dir", data, "--engines", "mock", "--dev-insecure",
                          "--api-keys", f"{KEY}:{OWNER}", "--rate-limit", "5", "--rate-burst", "5"],
                         stdout=log, stderr=subprocess.STDOUT)
    results = []
    try:
        assert wait_port(port), "stack did not start"
        hdr = {"Authorization": f"Bearer {KEY}"}

        code, _ = http(cport, "/internal/metrics")
        results.append(("PR09-01", code == 401, f"metrics without token → {code}"))

        body, hdrs = multipart("invoice.txt", (SENTINEL + "\nline two\n").encode())
        hdrs.update(hdr)
        code, resp = http(port, "/v1/jobs", "POST", body, hdrs)
        if code != 202:
            results.append(("PR09-02", False, f"create → {code} {resp[:150]}"))
            raise SystemExit(report(results))
        jid = json.loads(resp)["job_id"]
        deadline = time.time() + 60
        while time.time() < deadline:
            code, resp = http(port, f"/v1/jobs/{jid}", headers=hdr)
            if code == 200 and json.loads(resp)["processing_status"] in ("completed", "failed"):
                break
            time.sleep(0.2)

        code, text = http(cport, "/internal/metrics", headers={"X-Internal-Token": TOKEN})
        if code != 200:
            results.append(("PR09-02", False, f"metrics → {code}"))
        need = [
            "parsedock_jobs_created_total",
            "parsedock_jobs{",
            'parsedock_jobs{state="completed"} 1',
            "parsedock_queue_depth 0",
            "parsedock_disk_free_bytes",
            "parsedock_retention{",
            'parsedock_workers_online{device="cpu"} 1',
            "parsedock_cleanup_passes_total",
        ]
        missing = [n for n in need if n not in text]
        results.append(("PR09-02", not missing, f"missing series: {missing}" if missing else "lifecycle/queue/disk/worker series present"))

        results.append(("PR09-03", 'parsedock_parse_seconds_count{engine="mock"} 1' in text
                        and 'parsedock_result_bytes_total{engine="mock"}' in text,
                        "parse duration and result bytes recorded"))

        # Drain the rate limit: burst 5 was already partly used.
        throttled = 0
        for _ in range(20):
            c, _ = http(port, f"/v1/jobs/{jid}", headers=hdr)
            if c == 429:
                throttled += 1
        # Replicas batch refusals and flush every 5 s; poll instead of assuming the
        # aggregation is visible immediately.
        deadline = time.time() + 20
        text = ""
        while time.time() < deadline:
            _, text = http(cport, "/internal/metrics", headers={"X-Internal-Token": TOKEN})
            if 'parsedock_throttled_total{reason="rate_limited"}' in text:
                break
            time.sleep(0.5)
        results.append(("PR09-04", 'parsedock_throttled_total{reason="rate_limited"}' in text,
                        f"429s observed={throttled}, throttled series present"))

        leaks = [s for s in (SENTINEL, "invoice.txt", OWNER) if s in text]
        results.append(("PR09-05", not leaks, f"leaked: {leaks}" if leaks else "no content/name/owner in metrics"))
    finally:
        p.terminate()
        try:
            p.wait(5)
        except subprocess.TimeoutExpired:
            p.kill()
        log.close()
    return report(results)


def report(results):
    bad = [r for r in results if not r[1]]
    for name, ok, detail in results:
        print(f"{name} {'PASS' if ok else 'FAIL'} {detail}")
    print(f"\n{len(results) - len(bad)}/{len(results)} checks passed")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
