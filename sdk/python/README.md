# ParseDock Python SDK

```bash
pip install parsedock==0.1.1      # from a checkout: pip install -e sdk/python
```

One package, two entry points, no dependencies (stdlib only):

* `parsedock_sdk.ParseDockClient` — lifecycle wrapper: retries with backoff, polling,
  `result_version` handling, SSE, and "ACK only after your sink succeeded".
* `parsedock` — CLI for scripts and smoke tests.

```python
from parsedock_sdk import ParseDockClient, ParseDockError

with ParseDockClient("http://localhost:8080", token="…") as c:
    job = c.create_job("invoice.pdf", mode="auto", idempotency_key="invoice-42")
    final = c.wait(job["job_id"])                  # queued → running → completed
    if final["status"] != "completed":
        raise SystemExit(final.get("error_code"))

    ir, version = c.download(job["job_id"], fmt="json")       # Document IR

    def store(body: bytes) -> None:                # must return only once durably stored
        with open("out.md", "wb") as f:
            f.write(body)

    # Release only after the real consumer accepted the bytes:
    c.download_and_ack(job["job_id"], fmt="markdown", sink=store)
    c.wait_released(job["job_id"])                 # released / expired
```

Why `download` does not ACK by itself: an HTTP 200 means the bytes left the server, not
that you stored them. An ACK deletes the server copy. If your sink raises, the exception
propagates and the result stays downloadable until its TTL.

Errors are `ParseDockError(status, code, message)`. `.retryable` says whether a retry can
help, by code first: `rate_limited` is retryable, `quota_exceeded` (same 429) is not.

```bash
export PARSEDOCK_TOKEN=…
parsedock parse invoice.pdf -o out.md --api http://localhost:8080   # ACKs after writing
parsedock parse scan.pdf --mode accurate --keep                       # no ACK
parsedock status job_06GRV…
```

Exit codes: `0` success, `1` the job ended `failed`/`cancelled`, `2` request or connection
error.

## Reference

* `docs/SDK.md` — methods, lifecycle rules, CLI, test matrix, publishing status.
* `.agents/skills/parsedock-python-sdk/SKILL.md` — condensed reference (method table,
  error codes, how to read the Document IR).
* `docs/DOCUMENT-IR.md`, `docs/ERROR-CODES.md`, `api/openapi.yaml`.
* `examples/python` — runnable project (`make run`).

## Generated client

`../python-generated/` is produced from `api/openapi.yaml` by OpenAPI Generator
(`make sdk`) and gives typed models for every field. It is not published; regenerate it
after a spec change instead of editing it.

## Tests

`test_sdk.py` (lifecycle, against a live `docparse` with the mock engine),
`test_sdk_matrix.py` (error codes, retries, SSE resume) and `test_sdk_real.py` (real
engines). `make sdk-test` runs the lifecycle test; `bash scripts/sdk-check.sh --fast` is
what CI runs on every PR.
