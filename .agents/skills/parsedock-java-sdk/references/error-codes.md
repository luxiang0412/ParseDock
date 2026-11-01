<!-- Generated from ParseDock docs/ERROR-CODES.md (API 0.1.1) by scripts/sync_skills.py. Do not edit here. -->

# Error codes

Every error ParseDock returns uses one envelope:

```json
{"error": {"code": "gone", "message": "result released or expired"}}
```

`code` is a **public contract**: clients branch on it, the SDKs map it to a retry
policy and the playground maps it to a sentence. `message` is for humans and may
change. A failed job carries the same code in `error_code` on the job resource,
and `POST /v1/parse` returns a failed job as `422` with that code.

* **HTTP**: the status the code comes with as a response. "job" means it is (also)
  the `error_code` of a failed job (job resource, event stream, and `422` from
  `POST /v1/parse`).
* **Client retry**: whether retrying the same request can succeed (the Python SDK's
  `ParseDockError.retryable`). Honour `Retry-After` when present. A job's own
  `error_code` is final: the server already re-ran retryable attempts.

| Code | HTTP | Client retry | Meaning |
| --- | --- | --- | --- |
| `invalid_request` | 400 | no | Malformed request: bad JSON, bad multipart body (no file part, more than one, interrupted upload), unknown parameter value, missing `result_version` on ACK |
| `unsupported_type` | 415; job | no | File type not accepted by this deployment, content does not match the extension, or no engine handles the format |
| `file_too_large` | 413 | no | Upload exceeds the size limit (`--max-upload-mb`, default 64 MiB) |
| `malformed_file` | 400, 422; job | no | Damaged or structurally invalid document (empty file, broken ZIP/OLE2/PDF structure, unsafe archive paths), or an engine produced no content / invalid output |
| `encrypted_file` | 422; job | no | Password-protected document |
| `page_limit_exceeded` | 422 | no | Document has more pages than the configured page limit |
| `suspicious_compression` | 422 | no | Archive-based document (DOCX/XLSX/PPTX/…) has too many entries or a decompression ratio that looks like a zip bomb |
| `expanded_too_large` | 413; job | no | Document expands beyond the parser limits (e.g. very large XLSX); see "Deliberate gaps" in `AGENTS.md` |
| `engine_out_of_memory` | job | yes (job is retried by the coordinator first) | The engine process exceeded the RSS ceiling (`--engine-max-rss-mb`) and was killed |
| `quota_exceeded` | 429 (with `Retry-After`); job | no | Owner quota reached: concurrent jobs, jobs per day or stored bytes. Also raised at completion when the result would exceed the storage quota |
| `insufficient_storage` | 507 | yes | Coordinator disk is below the safety watermark; existing results stay readable |
| `engine_unavailable` | job | no | No usable engine for the job (sandbox requested but unavailable, engine server unreachable) |
| `gpu_unavailable` | 409; job | no | `gpu_required` job with no GPU worker / not enough free VRAM, or the attempt cannot prove it ran on a GPU |
| `timeout` | job | yes | Parse exceeded its time budget or the processing deadline passed |
| `cancelled` | job (internal: 409, 499) | no | Job was cancelled by the client; internally also the answer to a heartbeat or result upload of a cancelled job |
| `internal_error` | 500; job | yes | Unexpected server-side failure (database, filesystem, encoding) |
| `lease_lost` | job (internal: 409) | yes | The worker lost its lease (crash, network partition, expired or superseded lease); the attempt is retried and this code is final only when attempts are exhausted |
| `not_found` | 404 | no | Job does not exist for this owner, or a download lease no longer exists |
| `gone` | 410 | no | Result was released (ACK) or expired and can no longer be downloaded; worker input no longer available |
| `conflict` | 409 | no | Action not allowed in the current state: an idempotency key replayed on `POST /v1/parse` (use `/v1/jobs` for the existing job), stale or mismatching `result_version` on ACK, cancel/ACK on the wrong lifecycle state |
| `idempotency_key_mismatch` | 422 | no | The `Idempotency-Key` was already used by this owner for a different request: other input bytes, `mode` or `device` (`ttl_seconds` and `priority` are not compared). Nothing is created; use a new key |
| `unauthorized` | 401 | no | Missing or invalid API key (public) or internal token (internal API) |
| `insufficient_scope` | 403 | no | The API key is valid but lacks the scope the route needs (e.g. `jobs:ack`) |
| `rate_limited` | 429 (with `Retry-After`) | yes, after `Retry-After` | Per-owner request rate exceeded |
| `coordinator_unavailable` | 503 | yes | The API replica cannot reach the coordinator; the replica itself stays healthy |
