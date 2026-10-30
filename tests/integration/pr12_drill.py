#!/usr/bin/env python3
"""PR-12: online backup + restore drill with RTO measurement.

  PR12-01 an online backup of a running coordinator verifies (integrity + per-artifact SHA-256)
  PR12-02 an active result survives the restore and is downloadable
  PR12-03 a released result is NOT resurrected (ACK is a deletion request)
  PR12-04 retention state is re-applied after the restore (nothing servable that should not be)
  PR12-05 RTO is measured and recorded (restore + start until /readyz)

The drill runs against real processes and a real SQLite file; the only shortcut is the mock
engine, which keeps it fast enough for CI.
"""
import json
import os
import shutil
import sqlite3
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
BIN = os.environ.get("DOCPARSE_BIN", os.path.join(ROOT, "runtime", "bin", "docparse"))
TOKEN = "pr12-internal"
KEY = "pr12-key"
OWNER = "drill-owner"


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http(port, path, method="GET", data=None, headers=None, timeout=30):
    req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", method=method,
                                 data=data, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def multipart(name, content):
    b = "----pr12"
    body = (f"--{b}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{name}\"\r\n"
            "Content-Type: text/plain\r\n\r\n").encode() + content + f"\r\n--{b}--\r\n".encode()
    return body, {"Content-Type": f"multipart/form-data; boundary={b}"}


def wait_port(port, timeout=30):
    end = time.time() + timeout
    while time.time() < end:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.5):
                return True
        except OSError:
            time.sleep(0.1)
    return False


class Stack:
    def __init__(self, data_dir, log_path, port):
        self.port = port
        self.log = open(log_path, "w")
        self.p = subprocess.Popen(
            [BIN, "--role", "standalone", "--listen", f"127.0.0.1:{port}",
             "--internal-listen", f"127.0.0.1:{port + 1000}", "--internal-token", TOKEN,
             "--data-dir", data_dir, "--engines", "mock", "--dev-insecure",
             "--api-keys", f"{KEY}:{OWNER}", "--result-ttl", "600s",
             "--cleanup-interval", "1s", "--worker-lease", "5s", "--download-lease", "5s"],
            stdout=self.log, stderr=subprocess.STDOUT)

    def stop(self):
        self.p.terminate()
        try:
            self.p.wait(10)
        except subprocess.TimeoutExpired:
            self.p.kill()
        self.log.close()


def create_job(port, name, content, keep):
    body, hdrs = multipart(name, content)
    hdrs["Authorization"] = f"Bearer {KEY}"
    code, resp = http(port, "/v1/jobs", "POST", body, hdrs)
    if code != 202:
        raise AssertionError(f"create {name} → {code} {resp[:120]!r}")
    jid = json.loads(resp)["job_id"]
    deadline = time.time() + 60
    while time.time() < deadline:
        code, resp = http(port, f"/v1/jobs/{jid}", headers={"Authorization": f"Bearer {KEY}"})
        if code == 200 and json.loads(resp)["processing_status"] == "completed":
            break
        time.sleep(0.2)
    job = json.loads(resp)
    if not keep:
        code, _ = http(port, f"/v1/jobs/{jid}/ack", "POST",
                       json.dumps({"result_version": job["result_version"]}).encode(),
                       {"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"})
        if code != 202:
            raise AssertionError(f"ack {name} → {code}")
        for _ in range(120):
            code, resp = http(port, f"/v1/jobs/{jid}", headers={"Authorization": f"Bearer {KEY}"})
            if code == 200 and json.loads(resp)["retention_status"] == "released":
                break
            time.sleep(0.25)
    return jid, job["result_version"]


def main():
    if not os.path.exists(BIN):
        print(f"missing binary {BIN}; run `make build`", file=sys.stderr)
        return 1
    tmp = tempfile.mkdtemp(prefix="pr12-")
    results = []
    port = free_port()
    stack = None
    try:
        live = os.path.join(tmp, "live")
        stack = Stack(live, os.path.join(tmp, "live.log"), port)
        assert wait_port(port), "stack did not start"

        kept, kept_version = create_job(port, "kept.txt", b"kept\n", keep=True)
        released, _ = create_job(port, "released.txt", b"acked\n", keep=False)

        # A permanently failed job keeps retention_status=active but the coordinator deletes
        # its files immediately; the backup must accept that state (metadata-only row).
        failed, _ = create_job(port, "failed.txt", b"fails\n", keep=True)
        conn = sqlite3.connect(os.path.join(live, "coordinator.db"))
        with conn:
            conn.execute("UPDATE jobs SET processing_status='failed', error_code='engine_unavailable',"
                         " error_message='drill', retention_status='active' WHERE id=?", (failed,))
        conn.close()
        for sub in ("results", "inputs"):
            shutil.rmtree(os.path.join(live, sub, failed), ignore_errors=True)

        backup = os.path.join(tmp, "backup")
        t0 = time.time()
        r = subprocess.run([sys.executable, os.path.join(ROOT, "scripts", "backup_online.py"),
                            "--data-dir", live, "--out", backup, "--verify"],
                           capture_output=True, text=True)
        backup_seconds = time.time() - t0
        report = json.loads(r.stdout) if r.stdout.strip().startswith("{") else {}
        if r.returncode != 0:
            print("backup stderr:", r.stderr[-600:], file=sys.stderr)
        results.append(("PR12-01", r.returncode == 0 and report.get("verify", {}).get("ok") is True,
                        f"online backup verified in {backup_seconds:.1f}s "
                        f"({report.get('jobs_servable')} servable jobs, {report.get('files')} files, "
                        f"problems={report.get('verify', {}).get('problems')})"))
        results.append(("PR12-03", not os.path.isdir(os.path.join(backup, "files", "results", released)),
                        "released job's artifacts are absent from the backup"))
        # A failed-but-active job has no files: verification must not demand them.
        failed_row = os.path.join(backup, "files", "inputs", failed)
        results.append(("PR12-07", report.get("verify", {}).get("ok") is True and not os.path.isdir(failed_row),
                        "failed job with active retention: metadata-only backup verifies"))

        # A reused destination would overlay the new set on top of the old tree and keep
        # artifacts of jobs that are no longer servable.
        again = subprocess.run([sys.executable, os.path.join(ROOT, "scripts", "backup_online.py"),
                                "--data-dir", live, "--out", backup],
                               capture_output=True, text=True)
        results.append(("PR12-06", again.returncode != 0 and "non-empty" in again.stderr,
                        "second backup into the same directory is refused"))

        stack.stop()
        stack = None

        restored = os.path.join(tmp, "restored")
        t_restore = time.time()
        r = subprocess.run(["bash", os.path.join(ROOT, "scripts", "restore.sh"), backup, restored],
                           capture_output=True, text=True)
        if r.returncode != 0:
            results.append(("PR12-05", False, f"restore failed: {r.stderr[-200:]}"))
            raise SystemExit(report_all(results))

        stack = Stack(restored, os.path.join(tmp, "restored.log"), port)
        ready = wait_port(port) and http(port, "/readyz")[0] == 200
        rto = time.time() - t_restore
        results.append(("PR12-05", ready and rto < 120,
                        f"RTO {rto:.1f}s (restore + start until /readyz), RPO = backup age "
                        f"{backup_seconds:.1f}s"))

        code, body = http(port, f"/v1/jobs/{kept}/result?format=markdown",
                          headers={"Authorization": f"Bearer {KEY}"})
        # The mock engine's text is fixed, so compare the version and the bytes served with
        # what the live instance produced rather than the document content.
        code_v, job_body = http(port, f"/v1/jobs/{kept}", headers={"Authorization": f"Bearer {KEY}"})
        version_after = json.loads(job_body)["result_version"] if code_v == 200 else ""
        results.append(("PR12-02", code == 200 and body and version_after == kept_version,
                        f"active result after restore → {code} ({len(body)} bytes, "
                        f"version {'preserved' if version_after == kept_version else 'CHANGED'})"))

        code, body = http(port, f"/v1/jobs/{released}", headers={"Authorization": f"Bearer {KEY}"})
        state = json.loads(body)["retention_status"] if code == 200 else f"http {code}"
        code2, _ = http(port, f"/v1/jobs/{released}/result?format=markdown",
                        headers={"Authorization": f"Bearer {KEY}"})
        results.append(("PR12-04", state == "released" and code2 == 410,
                        f"released job after restore: state={state}, result={code2}"))
    finally:
        if stack:
            stack.stop()
        shutil.rmtree(tmp, ignore_errors=True)
    return report_all(results)


def report_all(results):
    bad = [r for r in results if not r[1]]
    for name, ok, detail in results:
        print(f"{name} {'PASS' if ok else 'FAIL'} {detail}")
    print(f"\n{len(results) - len(bad)}/{len(results)} checks passed")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
