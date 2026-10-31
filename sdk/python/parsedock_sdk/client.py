"""ParseDock Python SDK (Phase 0).

A small, dependency-free client that encodes the lifecycle rules ParseDock cares
about:

* create a job (multipart upload, idempotency key),
* poll or stream events until terminal,
* download a representation and remember its ``result_version``,
* **ack only after the caller's sink succeeded** (``download_and_ack``),
* idempotent ``ack`` / ``cancel``.

The generated OpenAPI client in ``sdk/python-generated`` is the machine-readable
surface; this module is the ergonomic wrapper (retries, backoff, lifecycle).

    from parsedock_sdk import ParseDockClient, ParseDockError

    with ParseDockClient("http://localhost:8080", token="…") as client:
        job = client.create_job("invoice.pdf", mode="accurate")
        data, version = client.download(job["job_id"], fmt="json")

        # Option A: explicit control
        client.ack(job["job_id"], version)

        # Option B: ack only after your own sink succeeded
        client.download_and_ack(job["job_id"], fmt="markdown", sink=open("out.md", "wb").write)
"""

from __future__ import annotations

import json
import mimetypes
import os
import random
import time
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Iterator

__all__ = ["ParseDockClient", "ParseDockError", "Job", "TERMINAL", "RETENTION_DONE"]

TERMINAL = {"completed", "failed", "cancelled"}
RETENTION_DONE = {"released", "expired"}
Job = dict[str, Any]


class ParseDockError(Exception):
    """Error returned by the service (stable ``code`` plus HTTP status)."""

    def __init__(self, status: int, code: str, message: str, retry_after: float | None = None):
        super().__init__(f"{status} {code}: {message}")
        self.status = status
        self.code = code
        self.message = message
        # Seconds the server asked us to wait (`Retry-After`), when it sent one.
        self.retry_after = retry_after

    # Codes that share a transient HTTP status but will not clear by retrying.
    # `quota_exceeded` is 429 like `rate_limited`, yet the quota only resets when
    # the rolling window passes or an operator changes it: retrying burns the
    # client's backoff budget and still fails.
    _NOT_RETRYABLE = frozenset({"quota_exceeded", "unsupported_type", "encrypted_file",
                                "file_too_large", "malformed_file", "invalid_request",
                                "page_limit_exceeded", "expanded_too_large", "gone",
                                "not_found", "gpu_unavailable", "engine_unavailable"})

    @property
    def retryable(self) -> bool:
        if self.code in self._NOT_RETRYABLE:
            return False
        return self.status in (429, 500, 502, 503, 504) or self.code in (
            "coordinator_unavailable",
            "insufficient_storage",
            "rate_limited",
        )


@dataclass
class _Response:
    status: int
    headers: dict[str, str]
    body: bytes


class ParseDockClient:
    def __init__(
        self,
        base_url: str,
        token: str,
        *,
        timeout: float = 120.0,
        max_retries: int = 4,
        user_agent: str = "parsedock-python-sdk/0.1.0",
    ) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = timeout
        self.max_retries = max_retries
        self.user_agent = user_agent

    # ------------------------------------------------------------------ plumbing
    def __enter__(self) -> "ParseDockClient":
        return self

    def __exit__(self, *exc: Any) -> None:
        pass

    def _headers(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        h = {"Authorization": f"Bearer {self.token}", "User-Agent": self.user_agent}
        if extra:
            h.update(extra)
        return h

    def _request(
        self,
        method: str,
        path: str,
        body: bytes | None = None,
        headers: dict[str, str] | None = None,
        *,
        timeout: float | None = None,
        retry: bool = True,
    ) -> _Response:
        url = self.base_url + path
        attempt = 0
        while True:
            req = urllib.request.Request(url, data=body, method=method,
                                         headers=self._headers(headers))
            try:
                with urllib.request.urlopen(req, timeout=timeout or self.timeout) as resp:
                    return _Response(resp.status, dict(resp.headers), resp.read())
            except urllib.error.HTTPError as e:
                raw = e.read()
                err = self._to_error(e.code, raw, dict(e.headers or {}))
                if retry and err.retryable and attempt < self.max_retries:
                    delay = min(2 ** attempt, 8) + random.random() / 4
                    if err.retry_after:
                        # The server knows when the window resets; do not guess.
                        delay = min(err.retry_after, 30)
                    time.sleep(delay)
                    attempt += 1
                    continue
                raise err
            except urllib.error.URLError as e:
                if retry and attempt < self.max_retries:
                    time.sleep(min(2 ** attempt, 8) + random.random() / 4)
                    attempt += 1
                    continue
                raise ParseDockError(502, "coordinator_unavailable", str(e)) from e

    @staticmethod
    def _to_error(status: int, raw: bytes, headers: dict[str, str] | None = None) -> ParseDockError:
        retry_after = None
        for k, v in (headers or {}).items():
            if k.lower() == "retry-after":
                try:
                    retry_after = float(str(v).strip())
                except ValueError:
                    retry_after = None
        try:
            payload = json.loads(raw.decode("utf-8", "replace"))
            err = payload.get("error") or {}
            return ParseDockError(status, err.get("code", "unknown"), err.get("message", ""),
                                  retry_after=retry_after)
        except Exception:
            return ParseDockError(status, "unknown", raw[:300].decode("utf-8", "replace"),
                                  retry_after=retry_after)

    @staticmethod
    def _multipart(fields: dict[str, str], path: str) -> tuple[bytes, str]:
        boundary = "----parsedock" + uuid.uuid4().hex
        name = Path(path).name
        ctype = mimetypes.guess_type(name)[0] or "application/octet-stream"
        out = bytearray()
        for k, v in fields.items():
            out += f"--{boundary}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n".encode()
        out += (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{name}\"\r\n"
                f"Content-Type: {ctype}\r\n\r\n").encode()
        with open(path, "rb") as f:
            out += f.read()
        out += b"\r\n" + f"--{boundary}--\r\n".encode()
        return bytes(out), f"multipart/form-data; boundary={boundary}"

    # ------------------------------------------------------------------ API
    def create_job(
        self,
        path: str | os.PathLike[str],
        *,
        mode: str = "auto",
        device: str = "auto",
        ttl_seconds: int | None = None,
        idempotency_key: str | None = None,
        priority: int | None = None,
    ) -> Job:
        """Upload and enqueue. Returns the job (``status=queued``).

        A key is always sent: the one you pass, or a generated one. The client
        retries failed requests, and without a key a retried upload whose first
        response was lost would create a second job that nobody releases. The
        generated key is stable for this logical upload (it lives in the multipart
        body built once), so a retry replays instead of duplicating.
        """
        fields = {"mode": mode, "device": device}
        if ttl_seconds is not None:
            fields["ttl_seconds"] = str(ttl_seconds)
        fields["idempotency_key"] = idempotency_key or uuid.uuid4().hex
        body, ctype = self._multipart(fields, str(path))
        q = ""
        if priority is not None:
            q = f"?priority={int(priority)}"
        resp = self._request("POST", "/v1/jobs" + q, body, {"Content-Type": ctype})
        return json.loads(resp.body)

    def get_job(self, job_id: str) -> Job:
        return json.loads(self._request("GET", f"/v1/jobs/{job_id}").body)

    def wait(
        self,
        job_id: str,
        *,
        timeout: float = 600.0,
        interval: float = 0.5,
        max_interval: float = 5.0,
    ) -> Job:
        """Poll until the job leaves queued/running. Returns the final job."""
        deadline = time.monotonic() + timeout
        delay = interval
        while True:
            job = self.get_job(job_id)
            if job.get("status") in TERMINAL:
                return job
            if time.monotonic() >= deadline:
                raise TimeoutError(f"job {job_id} still {job.get('status')} after {timeout}s")
            time.sleep(delay)
            delay = min(delay * 1.5, max_interval)

    def download(self, job_id: str, fmt: str = "markdown") -> tuple[bytes, str]:
        """Download a representation. Returns ``(body, result_version)``.

        The version must be passed to :meth:`ack`; it identifies exactly these bytes.
        """
        resp = self._request("GET", f"/v1/jobs/{job_id}/result?format={fmt}", timeout=600)
        # Only X-Result-Version identifies the result for ACK. The ETag validates
        # this representation's bytes and differs per format, so it must never be
        # used as the release token (F05).
        version = resp.headers.get("X-Result-Version", "")
        if not version:
            raise ParseDockError(500, "missing_result_version",
                                 "server did not return X-Result-Version; cannot ACK safely")
        return resp.body, version

    def download_and_ack(
        self,
        job_id: str,
        *,
        fmt: str = "markdown",
        sink: Callable[[bytes], Any] | None = None,
    ) -> bytes:
        """Download, hand the bytes to ``sink``, and ACK **only if the sink succeeded**.

        The sink's contract: when it returns, the bytes are *durably* stored. A
        buffered write that is never closed does not satisfy that - use a context
        manager (``with open(path, "wb") as f: f.write(body)``) or an API whose
        return means the write is committed. Raise from the sink if the write
        failed; the exception propagates, the result is not released, and the call
        can be retried.

        ``sink=None`` ACKs after the bytes are in this process's memory, which is
        only appropriate when the caller itself is the last consumer.
        """
        body, version = self.download(job_id, fmt=fmt)
        if sink is not None:
            sink(body)
        self.ack(job_id, version)
        return body

    def ack(self, job_id: str, result_version: str, *, action: str = "release",
            idempotency_key: str | None = None) -> dict[str, Any]:
        """Release the result. Idempotent; ``release_pending`` means "accepted"."""
        headers = {"Content-Type": "application/json"}
        if idempotency_key:
            headers["Idempotency-Key"] = idempotency_key
        body = json.dumps({"result_version": result_version, "action": action}).encode()
        return json.loads(self._request("POST", f"/v1/jobs/{job_id}/ack", body, headers,
                                        retry=True).body)

    def cancel(self, job_id: str) -> dict[str, Any]:
        """Cancel a queued/running job. Idempotent.

        A completed job cannot be cancelled and cancel never releases its result:
        the server returns ``409`` and you release with :meth:`ack`. Releasing is
        deliberately a single path so the outcome cannot depend on a race.
        """
        return json.loads(self._request("POST", f"/v1/jobs/{job_id}/cancel", None).body)

    def parse_sync(
        self,
        path: str | os.PathLike[str],
        *,
        mode: str = "auto",
        device: str = "auto",
        timeout_ms: int = 30000,
    ) -> dict[str, Any]:
        """Bounded synchronous parse. Returns the Document IR;
        if it returns a job instead (``202``), call ``wait()`` yourself."""
        body, ctype = self._multipart({"mode": mode, "device": device}, str(path))
        resp = self._request(
            "POST", f"/v1/parse?timeout_ms={int(timeout_ms)}", body, {"Content-Type": ctype},
            timeout=timeout_ms / 1000 + 30,
        )
        payload = json.loads(resp.body)
        if resp.status == 202:
            return {"async": True, "job": payload}
        return payload

    def events(self, job_id: str, *, last_event_id: int = 0) -> Iterator[dict[str, Any]]:
        """Yield SSE events until the server closes the stream."""
        headers = self._headers({"Accept": "text/event-stream"})
        if last_event_id:
            headers["Last-Event-ID"] = str(last_event_id)
        req = urllib.request.Request(self.base_url + f"/v1/jobs/{job_id}/events", headers=headers)
        with urllib.request.urlopen(req, timeout=600) as resp:
            current: dict[str, Any] = {}
            for raw in resp:
                line = raw.decode("utf-8", "replace").rstrip("\n")
                if line.startswith("id:"):
                    current["event_id"] = int(line[3:].strip())
                elif line.startswith("event:"):
                    current["event_type"] = line[6:].strip()
                elif line.startswith("data:"):
                    try:
                        current["data"] = json.loads(line[5:].strip())
                    except json.JSONDecodeError:
                        current["data"] = line[5:].strip()
                elif line == "" and current:
                    yield current
                    if current.get("event_type") == "stream_end":
                        return
                    current = {}

    def wait_released(self, job_id: str, *, timeout: float = 60.0) -> Job:
        """Wait until the cleaner finished releasing the artifact.

        Raises ``TimeoutError`` if retention did not reach a terminal state within
        ``timeout``: returning the still-pending job made callers announce a
        release that had not happened.
        """
        deadline = time.monotonic() + timeout
        while True:
            job = self.get_job(job_id)
            if job.get("retention_status") in RETENTION_DONE:
                return job
            if time.monotonic() >= deadline:
                raise TimeoutError(
                    f"job {job_id} still {job.get('retention_status')} after {timeout}s")
            time.sleep(0.5)
