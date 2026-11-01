---
name: parsedock-python-sdk
description: "ParseDock Python SDK (`parsedock_sdk`) and the `parsedock` CLI: upload a document, wait for the parse, download Markdown / plain text / Document IR, then ACK to release the server copy. Use when: calling a ParseDock service from Python; writing a script or service that converts PDF/DOCX/XLSX/PPTX/images to Markdown or structured JSON; polling job status; streaming job events over SSE; reading the Document IR (elements, rich_text, tables, furniture); handling ParseDock error codes such as rate_limited, quota_exceeded, insufficient_scope, encrypted_file or gpu_unavailable; or using the `parsedock` command line. Also use when the user mentions ParseDock, docparse, Document IR, doc-ir/0.1, result_version, or ACK/TTL behaviour."
---

# ParseDock Python SDK

Two things live in one package (`pip install parsedock`):

* `parsedock_sdk` — hand-written lifecycle wrapper, **stdlib only** (`ParseDockClient`).
* `parsedock` — CLI (`parsedock parse FILE`, `parsedock status JOB_ID`).

```bash
pip install parsedock==0.1.0      # or: pip install -e sdk/python (repo checkout)
parsedock --version               # prints "parsedock 0.1.0"
```

The API key is a bearer token. It needs the scopes for what you call: `jobs:write`
(upload, sync parse), `jobs:read` (status, download, events), `jobs:ack` (ack, cancel).
A key without an explicit scope list has all three.

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
**propagates a sink exception**, leaving the result available for a retry. The sink must
have *durably* stored the bytes when it returns (`with open(p, "wb") as f: f.write(b)`).

## Rules that are not optional

* **Never auto-ACK.** An ACK is a delete request. ACK exactly the `result_version`
  you downloaded: a stale one is refused with `409` and nothing is released.
* Without an ACK the result expires at `result_expires_at` (TTL).
* After release: `410 gone` for a short tombstone window, then `404`.
* `create_job` always sends an `Idempotency-Key` (yours or a generated one): a retried
  upload whose response was lost replays instead of creating a second job. Reusing *your*
  key for a different upload replays the first job.
* There is no job listing endpoint: keep the `job_id` you got from `create_job`.
* Status polling shares the per-owner rate budget; for long waits prefer `events()`.

## Methods

All arguments after the first are keyword-only.

| Method | Returns / notes |
| --- | --- |
| `create_job(path, *, mode="auto", device="auto", ttl_seconds=None, idempotency_key=None, priority=None)` | the job (`status="queued"`); `priority` 0..100 |
| `get_job(job_id)` | job dict: `status`, `engine`, `result_version`, `retention_status`, `error_code`, `error_message` |
| `wait(job_id, *, timeout=600, interval=0.5, max_interval=5)` | final job once `completed`/`failed`/`cancelled`; polls with backoff, raises `TimeoutError` |
| `download(job_id, fmt="markdown")` | `(bytes, result_version)`; `fmt` ∈ `markdown` \| `text` \| `json` |
| `download_and_ack(job_id, *, fmt="markdown", sink=None)` | the bytes; ACKs only after `sink` returns |
| `ack(job_id, result_version, *, action="release", idempotency_key=None)` | `202` → `retention_status="release_pending"`; idempotent |
| `cancel(job_id)` | queued/running only; a **completed** job returns `409` — releasing is ACK's job |
| `parse_sync(path, *, mode="auto", device="auto", timeout_ms=30000)` | the Document IR, or `{"async": True, "job": {...}}` when the server answered `202` (still running: call `wait`). The result is **not** released; ACK it or let it expire |
| `events(job_id, *, last_event_id=0)` | iterator of `{"event_id", "event_type", "data"}`; ends after `stream_end`; pass the last `event_id` to resume without replay |
| `wait_released(job_id, *, timeout=60)` | job once `retention_status` is `released`/`expired`; raises `TimeoutError` otherwise |

`mode`: `fast` (xberg, fastest), `accurate` (docling-rs, layout/structure; use it for
multi-column PDFs), `auto` (router decides, may retry a bad scan once on the accurate
engine). Text formats (`txt/md/csv/tsv`) always use the native engine.

`device`: `auto` | `cpu` | `gpu_preferred` (may fall back) | `gpu_required` (fails
`gpu_unavailable` unless the worker can prove GPU execution).

## Reading the Document IR (`fmt="json"`)

* `elements[]` is the reading order. `type`: `title`, `section` (with `level`),
  `paragraph`, `list_item` (with `level` for nesting), `table` (`table_id` → `tables[]`),
  `image`, `caption`, `code`, `header`, `footer`, …
* `text` is plain. `rich_text`, when present, is the same content with CommonMark inline
  markup (bold, italic, code, links); fall back to `text` when it is absent (always the
  case with a 0.1.0 server, which predates the field).
* Skip page furniture when you build your own text: `type` `header`/`footer`, or
  `metadata.content_layer == "furniture"`. The `markdown`/`text` downloads already do.
* `metadata` keys with a meaning (all added after 0.1.0): `content_layer="furniture"`,
  `markdown="blockquote"`, `list_ordered="true"`, `list_marker`, `code_language`.
  Unknown keys and element types may appear later; ignore them, do not fail.
* `tables[].cells[]`: `row`, `col`, `row_span`, `col_span`, `text`; `header_rows`.
* `warnings[].code` (`ocr_used`, `ocr_low_confidence`, `no_text_layer`, …) explain
  degraded output; `routing` says which engine ran and why.

Full reference: `docs/DOCUMENT-IR.md`.

## Errors

`ParseDockError(status, code, message)`. `e.retryable` tells you whether a retry can help;
the client already retries retryable errors with backoff (`max_retries=4`) and honours
`Retry-After`. A network failure surfaces as `coordinator_unavailable`.

| status | code | retryable | meaning |
| --- | --- | --- | --- |
| 400 | `invalid_request` | no | bad parameter or multipart body |
| 401 | `unauthorized` | no | bad or missing bearer token |
| 403 | `insufficient_scope` | no | key lacks `jobs:write` / `jobs:read` / `jobs:ack` |
| 404 | `not_found` | no | unknown job, another owner's job, or already deleted |
| 409 | `conflict` | no | stale `result_version` on ACK, ACK on a non-completed job, idempotency key reused |
| 409 | `gpu_unavailable` | no | `gpu_required` without a proven GPU worker |
| 410 | `gone` | no | released/expired (tombstone window) |
| 413 | `file_too_large` | no | over the deployment's upload cap |
| 415 | `unsupported_type` | no | format not enabled, or content does not match the extension |
| 422 | `encrypted_file`, `malformed_file`, `page_limit_exceeded` | no | the document itself cannot be parsed |
| 429 | `rate_limited` | yes | back off (`Retry-After`) |
| 429 | `quota_exceeded` | **no** | the owner's rolling quota is spent (`Retry-After` is still sent) |
| 503 | `coordinator_unavailable` | yes | the gateway cannot reach the coordinator |
| 507 | `insufficient_storage` | yes | server disk below its watermark |

A job that fails carries the same codes in `error_code` (plus `timeout`, `engine_*`,
`expanded_too_large`, …). The full table is `docs/ERROR-CODES.md`.

## CLI

```bash
export PARSEDOCK_TOKEN=…                 # or --token; --api defaults to http://127.0.0.1:8080
parsedock parse invoice.pdf --format markdown -o out.md --api http://host:8080
parsedock parse scan.pdf --mode accurate --keep    # do not ACK; result stays downloadable
parsedock status job_06GRV…              # job as JSON
```

`parse` prints the document to stdout (or `-o`), the job id/version to stderr, and ACKs
after writing unless `--keep`. Exit codes: `0` success, `1` the job ended
`failed`/`cancelled`, `2` any request error (HTTP error such as 401/413, or no connection).

## More

`docs/SDK.md` (contract, testing matrix), `docs/DOCUMENT-IR.md` (every IR field),
`docs/ERROR-CODES.md`, `api/openapi.yaml`, `examples/python` (`make run`).
