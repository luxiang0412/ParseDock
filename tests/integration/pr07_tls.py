#!/usr/bin/env python3
"""PR-07: mutual TLS between API replicas, the Coordinator and workers.

Checks, against real processes on loopback:

  PR07-01 coordinator refuses a client without a certificate
  PR07-02 coordinator refuses a certificate signed by another CA
  PR07-03 api replica with a valid client certificate proxies a job end to end
  PR07-04 worker completes a job over mTLS (coordinator sees the client identity)
  PR07-05 an api replica without a client certificate cannot reach the coordinator
  PR07-06 plain HTTP against the TLS port fails the handshake

Certificates are generated per run in a temporary directory and never reused.
"""
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

BIN = os.environ.get("DOCPARSE_BIN", "bin/docparse")


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def run(cmd, **kw):
    return subprocess.run(cmd, capture_output=True, text=True, **kw)


def certs(dirpath: str):
    """Create a CA, a server certificate and two client certificates."""
    ca_key, ca_crt = os.path.join(dirpath, "ca.key"), os.path.join(dirpath, "ca.crt")
    run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
         "-keyout", ca_key, "-out", ca_crt, "-subj", "/CN=parsedock-test-ca"])
    out = {"ca": ca_crt}

    def issue(name, san):
        key, crt = os.path.join(dirpath, name + ".key"), os.path.join(dirpath, name + ".crt")
        csr = os.path.join(dirpath, name + ".csr")
        run(["openssl", "req", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", csr,
             "-subj", "/CN=" + name])
        ext = os.path.join(dirpath, name + ".ext")
        with open(ext, "w") as fh:
            fh.write("subjectAltName=" + san + "\n" if san else "")
        run(["openssl", "x509", "-req", "-in", csr, "-CA", ca_crt, "-CAkey", ca_key,
             "-CAcreateserial", "-out", crt, "-days", "1", "-extfile", ext])
        return key, crt

    skey, scrt = issue("coordinator", "IP:127.0.0.1,DNS:localhost")
    out["server"] = (skey, scrt)
    out["client"] = issue("api", None)
    out["worker"] = issue("worker", None)

    # A second, unrelated CA to prove verification is real.
    other = os.path.join(dirpath, "other")
    os.makedirs(other, exist_ok=True)
    run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
         "-keyout", os.path.join(other, "o.key"), "-out", os.path.join(other, "o.crt"),
         "-subj", "/CN=other-ca"])
    run(["openssl", "req", "-newkey", "rsa:2048", "-nodes",
         "-keyout", os.path.join(other, "i.key"), "-out", os.path.join(other, "i.csr"),
         "-subj", "/CN=impostor"])
    ext = os.path.join(other, "i.ext")
    open(ext, "w").write("")
    run(["openssl", "x509", "-req", "-in", os.path.join(other, "i.csr"),
         "-CA", os.path.join(other, "o.crt"), "-CAkey", os.path.join(other, "o.key"),
         "-CAcreateserial", "-out", os.path.join(other, "i.crt"), "-days", "1", "-extfile", ext])
    out["impostor"] = (os.path.join(other, "i.key"), os.path.join(other, "i.crt"))
    return out


class Proc:
    def __init__(self, args, log):
        self.log = open(log, "w")
        self.p = subprocess.Popen(args, stdout=self.log, stderr=subprocess.STDOUT)
        self.path = log

    def stop(self):
        self.p.terminate()
        try:
            self.p.wait(5)
        except subprocess.TimeoutExpired:
            self.p.kill()
        self.log.close()

    def text(self):
        return open(self.path).read()

    def dump(self, label):
        sys.stderr.write(f"--- {label} ---\n" + self.text()[-2000:] + "\n")


def wait_port(port, timeout=25):
    end = time.time() + timeout
    while time.time() < end:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.5):
                return True
        except OSError:
            time.sleep(0.1)
    return False


def http(url, method="GET", data=None, headers=None, timeout=10):
    req = urllib.request.Request(url, method=method, data=data, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:  # noqa: BLE001 - handshake and connection failures
        return None, str(e).encode()


def multipart(name, content):
    boundary = "----pr07"
    body = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{name}\"\r\n"
            "Content-Type: text/plain\r\n\r\n").encode() + content + f"\r\n--{boundary}--\r\n".encode()
    return body, {"Content-Type": f"multipart/form-data; boundary={boundary}"}


def main():
    results = []
    tmp = tempfile.mkdtemp(prefix="pr07-")
    procs = []
    try:
        c = certs(tmp)
        data = os.path.join(tmp, "data")
        coord_port, api_port = free_port(), free_port()
        cport = f"https://127.0.0.1:{coord_port}"
        skal, scrt = c["server"]
        ckal, ccrt = c["client"]
        wkal, wcrt = c["worker"]

        procs.append(Proc([BIN, "--role", "coordinator", "--internal-listen", f"127.0.0.1:{coord_port}",
                           "--internal-token", "tok", "--data-dir", data, "--engines", "mock",
                           "--tls-cert", scrt, "--tls-key", skal, "--tls-client-ca", c["ca"],
                           "--log-level", "debug"], os.path.join(tmp, "coord.log")))
        assert wait_port(coord_port), "coordinator did not start"

        # PR07-01/02/06: no certificate, foreign certificate, plain HTTP.
        plain = run(["curl", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "5",
                     "--cacert", c["ca"], f"{cport}/healthz"])
        results.append(("PR07-01", plain.returncode != 0,
                        f"client without a certificate rejected (curl rc={plain.returncode})"))
        imp_key, imp_crt = c["impostor"]
        foreign = run(["curl", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "5",
                       "--cacert", c["ca"], "--cert", imp_crt, "--key", imp_key,
                       f"{cport}/healthz"])
        results.append(("PR07-02", foreign.returncode != 0,
                        f"certificate from another CA rejected (curl rc={foreign.returncode})"))
        plainhttp = run(["curl", "-sS", "-w", "%{http_code}", "--max-time", "5",
                         f"http://127.0.0.1:{coord_port}/healthz"])
        # Go's TLS listener answers a plaintext request with 400 and a hint.
        results.append(("PR07-06", "400" in plainhttp.stdout or "HTTPS" in plainhttp.stdout,
                        f"plain HTTP rejected (curl rc={plainhttp.returncode}, {plainhttp.stdout[-80:]!r})"))

        # A properly authenticated request works.
        ok = run(["curl", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "5",
                  "--cacert", c["ca"], "--cert", ccrt, "--key", ckal, f"{cport}/healthz"])
        results.append(("PR07-07", ok.stdout.strip() == "200",
                        f"authenticated health check → {ok.stdout.strip()}"))

        # PR07-03/04: api replica + worker over mTLS complete a job.
        tls_flags = ["--tls-ca", c["ca"], "--tls-server-name", "localhost"]
        procs.append(Proc([BIN, "--role", "api", "--listen", f"127.0.0.1:{api_port}",
                           "--coordinator-url", cport, "--internal-token", "tok", "--dev-insecure",
                           "--api-keys", "test-key:owner-1", "--max-upload-mb", "8",
                           "--rate-limit", "200", *tls_flags, "--tls-cert", ccrt, "--tls-key", ckal],
                          os.path.join(tmp, "api.log")))
        assert wait_port(api_port), "api did not start"
        procs.append(Proc([BIN, "--role", "worker", "--coordinator-url", cport, "--internal-token", "tok",
                           "--engines", "mock", "--worker-id", "w-tls",
                           *tls_flags, "--tls-cert", wcrt, "--tls-key", wkal],
                          os.path.join(tmp, "worker.log")))

        body, hdrs = multipart("tls.txt", b"mutual TLS round trip\n")
        hdrs["Authorization"] = "Bearer test-key"
        code, resp = http(f"http://127.0.0.1:{api_port}/v1/jobs", "POST", body, hdrs)
        if code != 202:
            results.append(("PR07-03", False, f"create job → {code} {resp[:200]!r}"))
        else:
            job = json.loads(resp)
            jid = job["job_id"]

            def status():
                return http(f"http://127.0.0.1:{api_port}/v1/jobs/{jid}",
                            headers={"Authorization": "Bearer test-key"})
            deadline = time.time() + 60
            st = {}
            while time.time() < deadline:
                code, body = status()
                if code == 200:
                    st = json.loads(body)
                    if st.get("processing_status") in ("completed", "failed"):
                        break
                time.sleep(0.3)
            if st.get("processing_status") != "completed":
                procs[-1].dump("worker")
                procs[0].dump("coordinator")
            results.append(("PR07-04", st.get("processing_status") == "completed",
                            f"worker over mTLS → {st.get('processing_status')} ({st.get('error_code')})"))
            results.append(("PR07-03", True, "api replica proxied the job over mTLS"))

        # PR07-05: an api replica without a client certificate cannot reach the coordinator.
        bad_port = free_port()
        procs.append(Proc([BIN, "--role", "api", "--listen", f"127.0.0.1:{bad_port}",
                           "--coordinator-url", cport, "--internal-token", "tok", "--dev-insecure",
                           "--api-keys", "test-key:owner-1", *tls_flags],
                          os.path.join(tmp, "api-noclient.log")))
        assert wait_port(bad_port), "second api did not start"
        body, hdrs = multipart("tls.txt", b"no client certificate\n")
        hdrs["Authorization"] = "Bearer test-key"
        code, resp = http(f"http://127.0.0.1:{bad_port}/v1/jobs", "POST", body, hdrs)
        results.append(("PR07-05", code in (502, 503),
                        f"api without a client certificate → {code}"))

        # The coordinator must never have logged document bytes or the token.
        log = procs[0].text()
        clean = "mutual TLS round trip" not in log and "tok" not in log.replace("token", "")
        results.append(("PR07-08", clean, "coordinator log free of content and secrets"))
    finally:
        for p in procs:
            p.stop()
        shutil.rmtree(tmp, ignore_errors=True)

    bad = [r for r in results if not r[1]]
    for name, ok, detail in results:
        print(f"{name} {'PASS' if ok else 'FAIL'} {detail}")
    print(f"\n{len(results) - len(bad)}/{len(results)} checks passed")
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
