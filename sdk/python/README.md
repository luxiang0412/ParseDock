# ParseDock Python SDK

Two layers:

1. **Hand-written lifecycle wrapper** — `parsedock_sdk` (no dependencies, stdlib only).
   Encodes retries, backoff, polling, `result_version` handling, SSE and the
   "ack only after your sink succeeded" rule.
2. **Generated client** — `../python-generated/` produced from `api/openapi.yaml`
   by OpenAPI Generator (`make sdk`). Use it for typed models of every IR field;
   regenerate after any spec change instead of editing it.

```python
from parsedock_sdk import ParseDockClient

with ParseDockClient("http://localhost:8080", token="…") as c:
    job = c.create_job("invoice.pdf", mode="accurate", idempotency_key="invoice-42")
    final = c.wait(job["job_id"])                  # queued → running → completed
    if final["status"] != "completed":
        raise SystemExit(final.get("error_code"))

    ir, version = c.download(job["job_id"], fmt="json")

    # Release only after the real consumer accepted the bytes:
    c.download_and_ack(job["job_id"], fmt="markdown",
                       sink=lambda b: open("out.md", "wb").write(b))

    c.wait_released(job["job_id"])                 # released / expired
```

Why `download_and_ack` is not automatic in `download`: an HTTP 200 means the
bytes left the server, not that you stored them. If your sink raises, the
exception propagates and the result stays available for a retry until its TTL.

Errors are `ParseDockError(status, code, message)` with `retryable` telling you
whether a retry can help (`429/5xx`, `coordinator_unavailable`).

## Tested in Phase 0

`sdk/python/test_sdk.py` runs the lifecycle against a live stack
(create → wait → download → ack → 410 → released) and asserts that a failing
sink does **not** release the result. Run it with `make sdk-test`.
