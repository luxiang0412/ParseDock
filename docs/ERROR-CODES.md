# Error codes

Every error ParseDock returns uses one envelope:

```json
{"error": {"code": "gone", "message": "result released or expired"}}
```

`code` is a **public contract**: clients branch on it, the SDKs map it to a retry
policy and the playground maps it to a sentence. `message` is for humans and may
change. A failed job carries the same code in `error_code` on the job resource,
and `POST /v1/parse` returns a failed job as `422` with that code.

The set of codes is defined once in `internal/jobs/job.go` and must be identical in
three places, which `tests/contract/openapi_contract.py` checks on every PR:

1. the constants in `internal/jobs/job.go`,
2. the `Error.code` enum in `api/openapi.yaml` (and therefore the generated SDKs and
   `web/src/api/schema.d.ts`),
3. the table below.

Adding a code: add the constant, the enum entry and a row here in the same change,
then run `make sdk` and `cd web && npm run api:types`. Removing or renaming a code
is a breaking API change.

## Columns

* **HTTP** — the status the code is returned with when it is the response to a
  request. "job" means the code is (also) recorded as `error_code` of a failed job
  (then visible through `GET /v1/jobs/{id}`, the event stream, and `422` from
  `POST /v1/parse`).
* **Client retry** — whether retrying the *same request* can succeed. This is the
  policy of the Python SDK (`ParseDockError.retryable` in
  `sdk/python/parsedock_sdk/client.py`); honour `Retry-After` when present.
  Independently, the coordinator re-runs a job attempt itself when the worker marks
  a failure retryable (up to `max_attempts`), so a "job" code a client sees is
  already final for that job.
* **Origin** — where the code is produced.

## Codes

| Code | HTTP | Client retry | Meaning | Origin |
| --- | --- | --- | --- | --- |
| `invalid_request` | 400 | no | Malformed request: bad JSON, bad multipart body (no file part, more than one, interrupted upload), unknown parameter value, missing `result_version` on ACK | coordinator HTTP layer and service |
| `unsupported_type` | 415; job | no | File type not accepted by this deployment, content does not match the extension, or no engine handles the format | `internal/storage` sniffing, coordinator routing, worker (no engine), `engines.ClassifyCLIError` |
| `file_too_large` | 413 | no | Upload exceeds the size limit (`--max-upload-mb`, default 64 MiB) | coordinator upload / storage validation |
| `malformed_file` | 400, 422; job | no | Damaged or structurally invalid document (empty file, broken ZIP/OLE2/PDF structure, unsafe archive paths), or an engine produced no content / invalid output | `internal/storage` sniffing, engines, worker, coordinator result validation |
| `encrypted_file` | 422; job | no | Password-protected document | coordinator pre-flight, `engines.ClassifyCLIError` |
| `page_limit_exceeded` | 422 | no | Document has more pages than the configured page limit | `internal/storage` PDF pre-flight |
| `suspicious_compression` | 422 | no | Archive-based document (DOCX/XLSX/PPTX/…) has too many entries or a decompression ratio that looks like a zip bomb | `internal/storage` ZIP inspection |
| `expanded_too_large` | 413; job | no | Document expands beyond the parser limits (e.g. very large XLSX); see "Deliberate gaps" in `AGENTS.md` | `internal/storage` ZIP inspection, engines (xberg, docling-rs), `engines.ClassifyCLIError` |
| `engine_out_of_memory` | job | yes (job is retried by the coordinator first) | The engine process exceeded the RSS ceiling (`--engine-max-rss-mb`) and was killed | `internal/engines/exec.go` RSS watchdog (xberg, docling-rs, mineru) |
| `quota_exceeded` | 429 (with `Retry-After`); job | no | Owner quota reached: concurrent jobs, jobs per day or stored bytes. Also raised at completion when the result would exceed the storage quota | coordinator quota admission (`quota.go`) and completion |
| `insufficient_storage` | 507 | yes | Coordinator disk is below the safety watermark; existing results stay readable | coordinator upload |
| `engine_unavailable` | job | no | No usable engine for the job (sandbox requested but unavailable, engine server unreachable) | engines (xberg, docling-rs, docling-serve, tika, mineru) |
| `gpu_unavailable` | 409; job | no | `gpu_required` job with no GPU worker / not enough free VRAM, or the attempt cannot prove it ran on a GPU | coordinator routing, worker |
| `timeout` | job | yes | Parse exceeded its time budget or the processing deadline passed | engines, worker, coordinator cleanup (deadline sweep) |
| `cancelled` | job (internal: 409, 499) | no | Job was cancelled by the client; internally also the answer to a heartbeat or result upload of a cancelled job | worker, coordinator |
| `internal_error` | 500; job | yes | Unexpected server-side failure (database, filesystem, encoding) | anywhere |
| `lease_lost` | job (internal: 409) | yes | The worker lost its lease (crash, network partition, expired or superseded lease); the attempt is retried and this code is final only when attempts are exhausted | coordinator lease sweep, worker protocol |
| `not_found` | 404 | no | Job does not exist for this owner, or a download lease no longer exists | coordinator |
| `gone` | 410 | no | Result was released (ACK) or expired and can no longer be downloaded; worker input no longer available | coordinator |
| `conflict` | 409 | no | Action not allowed in the current state: idempotency key reuse, stale or mismatching `result_version` on ACK, cancel/ACK on the wrong lifecycle state | coordinator |
| `unauthorized` | 401 | no | Missing or invalid API key (public) or internal token (internal API) | API gateway, coordinator internal auth |
| `insufficient_scope` | 403 | no | The API key is valid but lacks the scope the route needs (e.g. `jobs:ack`) | API gateway (`internal/api/server.go`) |
| `rate_limited` | 429 (with `Retry-After`) | yes, after `Retry-After` | Per-owner request rate exceeded | API gateway rate limiter |
| `coordinator_unavailable` | 502 | yes | The API replica cannot reach the coordinator; the replica itself stays healthy. `api/openapi.yaml` documents this response as `503`, the gateway currently sends `502` | API gateway reverse proxy (`internal/api/server.go`) |

Codes not in this table are not part of the contract. A client that receives an
unknown code should treat it like `internal_error` (the generated SDKs are built with
`enumUnknownDefaultCase=true`, so they do not crash on one).
