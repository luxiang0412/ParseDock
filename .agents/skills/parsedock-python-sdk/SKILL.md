---
name: parsedock-python-sdk
description: "ParseDock Python SDK (`parsedock_sdk`) and the `parsedock` CLI: upload a document, wait for the parse, download Markdown / plain text / Document IR, then ACK to release the server copy. Use when: calling a ParseDock service from Python; writing a script or service that converts PDF/DOCX/XLSX/PPTX/images to Markdown or structured JSON; polling job status; streaming job events over SSE; handling ParseDock error codes such as rate_limited, quota_exceeded, encrypted_file or gpu_unavailable; or using the `parsedock` command line. Also use when the user mentions ParseDock, docparse, Document IR, doc-ir/0.1, result_version, or ACK/TTL behaviour."
---

# ParseDock Python SDK

Two things live in one package:

* `parsedock_sdk` — hand-written lifecycle wrapper, **stdlib only** (`ParseDockClient`).
* `parsedock` — CLI (`parsedock parse FILE`).

Install from a checkout (not on PyPI yet):

```bash
pip install -e sdk/python      # or: pip install parsedock  once published
parsedock --version            # JSON: {"version": "...", "api": "..."}
```

## The five-step lifecycle

```python
from parsedock_sdk import ParseDockClient, ParseDockError

with ParseDockClient("http://localhost:8080", token=TOKEN) as c:
    job = c.create_job("invoice.pdf", mode="auto", idempotency_key="invoice-42")
    final = c.wait(job["job_id"], timeout=600)          # queued → running → completed
    if final["status"] != "completed":
        raise SystemExit(final.get("error_code"))

    md, version = c.download(job["job_id"], fmt="markdown")   # bytes, result_version
    ir, _ = c.download(job["job_id"], fmt="json")             # Document IR (doc-ir/0.1)

    # ACK only after YOUR sink stored the bytes. 200 means "bytes left the server".
    c.ack(job["job_id"], version)
    c.wait_released(job["job_id"])                            # released | expired
```

`download_and_ack(job_id, fmt=..., sink=callable)` does download → sink → ACK and
**propagates a sink exception**, leaving the result available for a retry.

## Rules that are not optional

* **Never auto-ACK.** An ACK is a delete request. ACK exactly the `result_version`
  you downloaded: a stale one is refused with `409` and nothing is released.
* Without an ACK the result expires at `result_expires_at` (TTL).
* After release: `410 gone` for a short tombstone window, then `404`.
* Rows listed in `GET /v1/jobs` never include the text; download is separate.

## Methods

| Method | Notes |
| --- | --- |
| `create_job(path, mode="auto", device="auto", ttl_seconds=None, idempotency_key=None, priority=None)` | 202 → `status="queued"`; the same idempotency key replays the original job |
| `get_job(id)` | status, engine, `result_version`, `retention_status`, `error_code` |
| `wait(id, timeout=600, interval=0.5, max_interval=5)` | polls with backoff, raises `TimeoutError` |
| `download(id, fmt="markdown")` | `fmt` ∈ `markdown` \| `text` \| `json`; returns `(bytes, version)` |
| `download_and_ack(id, fmt=..., sink=...)` | ACKs only after the sink returns |
| `ack(id, result_version, action="release")` | `202` → `release_pending`; idempotent |
| `cancel(id, result_version=None)` | queued/running only; a finished job must be ACKed |
| `parse_sync(path, ...)` | create + wait + download in one call (small files) |
| `events(id, last_event_id=0)` | SSE iterator; pass the last id to resume without replay |
| `wait_released(id, timeout=60)` | until `retention_status` is `released` |

`mode`: `fast` (xberg, fastest), `accurate` (docling-rs, structure), `auto` (router
decides, may retry once on the accurate engine for a bad scan). Text formats
(`txt/md/csv/tsv`) always use the native engine.

`device`: `auto` | `cpu` | `gpu_preferred` (may fall back) | `gpu_required` (fails
`gpu_unavailable` unless the worker can prove GPU execution).

## Errors

`ParseDockError(status, code, message)`; `e.retryable` tells you whether a retry
can help (the client already retries retryable errors with backoff, `max_retries=4`).

| status | code | meaning |
| --- | --- | --- |
| 401 | `unauthorized` | bad or missing bearer token |
| 404 | `not_found` | unknown job, another owner's job, or already deleted |
| 409 | `conflict` | stale `result_version` on ACK, or ACK on a non-completed job |
| 410 | `gone` | released/expired (tombstone window) |
| 413 | `file_too_large` | over the deployment's upload cap |
| 415 | `unsupported_type` | format not enabled |
| 422 | `encrypted_file` | password-protected document |
| 429 | `rate_limited` | retryable; back off |
| 429 | `quota_exceeded` | **not** retryable: the owner's rolling quota is spent |
| 409 | `gpu_unavailable` | `gpu_required` without a proven GPU worker |

## CLI

```bash
parsedock parse invoice.pdf --format markdown -o out.md \
  --api http://localhost:8080 --token "$PARSEDOCK_TOKEN"
parsedock parse scan.pdf --keep          # do not ACK; result stays downloadable
```

Exit codes: `0` success, `1` server/parse error, `2` connection error.

## More

`docs/SDK.md` (contract, testing matrix), `api/openapi.yaml` (every field),
`sdk/python/README.md` (wrapper vs generated client).
