---
name: parsedock-java-sdk
description: "ParseDock Java client (`com.luxiangs:parsedock-client`): upload a document with JobsApi, poll the job, download Markdown or the typed DocumentIR model, then ACK with the exact result_version to release the server copy. Use when: calling a ParseDock service from Java, Kotlin, Scala, Groovy or any JVM project; building an integration with Maven or Gradle; deserialising the Document IR (doc-ir/0.1) into generated model classes; streaming a large result to disk; handling ApiException codes such as rate_limited, quota_exceeded, insufficient_scope, file_too_large, encrypted_file or gpu_unavailable; or writing an end-to-end test against a running ParseDock instance. Also use when the user mentions ParseDock, JobsApi, DocumentIR, result_version, or ACK/TTL from a JVM context."
---

# ParseDock Java SDK

Applies to ParseDock API `0.1` (server and SDK 0.1.x). Fields added after the 0.1.0
release are marked; against a 0.1.0 server they are simply absent.

The client is generated from the public OpenAPI document with OpenAPI Generator
(OkHttp + Gson): every endpoint is a method, every schema a model class.

Published on Maven Central (Java 8+; CI builds and tests on JDK 17):

```xml
<dependency>
  <groupId>com.luxiangs</groupId>
  <artifactId>parsedock-client</artifactId>
  <version>0.1.0</version>
</dependency>
```

Gradle: `implementation("com.luxiangs:parsedock-client:0.1.0")`.

The API key is a bearer token. It needs the scopes for what you call: `jobs:write`
(upload, sync parse), `jobs:read` (status, download, events), `jobs:ack` (ack, cancel).
A key without an explicit scope list has all three.

## The five-step lifecycle

```java
import java.io.File;
import org.openapitools.client.ApiClient;
import org.openapitools.client.JSON;
import org.openapitools.client.api.JobsApi;
import org.openapitools.client.model.AckJobRequest;
import org.openapitools.client.model.DocumentIR;
import org.openapitools.client.model.Job;

ApiClient client = new ApiClient();
client.setBasePath("http://localhost:8080");
client.setBearerToken(System.getenv("PARSEDOCK_TOKEN"));
client.setReadTimeout(600_000);               // ms; large results take a while
JobsApi api = new JobsApi(client);

// createJob(File, mode, device, ttlSeconds, priority, idempotencyKey,
//           mode2, device2, ttlSeconds2, idempotencyKey2)
// The second set is the query-parameter mirror of the same fields: pass nulls.
Job job = api.createJob(new File("invoice.pdf"), "auto", "auto",
        null, null, null, null, null, null, null);

long deadline = System.currentTimeMillis() + 600_000;
Job done = api.getJob(job.getJobId());
long delay = 300;
while (done.getStatus() == Job.StatusEnum.QUEUED || done.getStatus() == Job.StatusEnum.RUNNING) {
    if (System.currentTimeMillis() > deadline) {
        throw new IllegalStateException("job " + job.getJobId() + " still " + done.getStatus());
    }
    Thread.sleep(delay);
    delay = Math.min(delay * 3 / 2, 5_000);   // polling shares the per-owner rate budget
    done = api.getJob(job.getJobId());
}
// COMPLETED, FAILED, CANCELLED - or UNKNOWN_DEFAULT_OPEN_API for a status this client
// version does not know: treat anything but COMPLETED as "no result".
if (done.getStatus() != Job.StatusEnum.COMPLETED) {
    throw new IllegalStateException(done.getErrorCode() + ": " + done.getErrorMessage());
}

String markdown = api.downloadResult(done.getJobId(), "markdown");       // "text" | "json" too
DocumentIR ir = JSON.deserialize(api.downloadResult(done.getJobId(), "json"), DocumentIR.class);

// ACK only after YOUR sink stored the bytes; 200 means "bytes left the server".
api.ackJob(done.getJobId(),
           new AckJobRequest().resultVersion(done.getResultVersion()), null);
```

## Rules that are not optional

* **Never auto-ACK.** `ackJob` is a delete request. It must carry the exact
  `result_version` you downloaded (`Job#getResultVersion()`, or the
  `X-Result-Version` response header). A stale value → `409`, nothing released.
* Without an ACK the result expires at `result_expires_at` (TTL).
* `cancelJob(id, null)` only stops queued/running work: on a completed job it returns
  `409` and never releases the result.
* After release: `410` during the tombstone window, then `404`.
* There is no job listing endpoint: keep the job id from `createJob`.
* Reusing an `idempotencyKey` replays the first job instead of creating a new one.

## Large results: stream to disk

`downloadResult` and `downloadResultWithHttpInfo` both return the whole body as a
`String`. To stream, execute the raw OkHttp call:

```java
okhttp3.Call call = api.downloadResultCall(jobId, "json", null);
try (okhttp3.Response r = call.execute()) {
    if (!r.isSuccessful()) throw new IllegalStateException("download " + r.code());
    String version = r.header("X-Result-Version");          // the ACK token
    java.nio.file.Files.copy(r.body().byteStream(), out,
            java.nio.file.StandardCopyOption.REPLACE_EXISTING);  // out: a java.nio.file.Path
    api.ackJob(jobId, new AckJobRequest().resultVersion(version), null);  // after the copy
}
```

## Other endpoints

* `SyncApi#parseSync(file, mode, device, timeoutMs)`: bounded synchronous parse for small
  files; the result is **not** released (ACK it or let it expire). When the parse outlives
  `timeoutMs` the server answers `202` with a Job, which this typed method cannot carry:
  check `parseSyncWithHttpInfo(...).getStatusCode()`, or use `createJob` for anything
  that may be slow.
* `EventsApi#jobEvents(id, scope, lastEventID, lastEventId)`: the SSE stream, returned as
  one `String` when the server closes it (`scope=processing` ends after the terminal
  status). For live events use an SSE client against `GET /v1/jobs/{id}/events`.
* `ProbesApi#healthz()` / `readyz()`.

## Reading the DocumentIR

* `getElements()` is the reading order. `getType()`: `title`, `section` (`getLevel()`),
  `paragraph`, `list_item` (`getLevel()` = nesting), `table` (`getTableId()` →
  `getTables()`), `image`, `caption`, `code`, `header`, `footer`, …
* `getText()` is plain; `getRichText()`, when not null, is the same content with
  CommonMark inline markup (bold, italic, code, links). `rich_text` was added after 0.1.0:
  the 0.1.0 artifact has no `getRichText()` (Gson ignores the field), and a 0.1.0 server
  never sends it. Use `getText()` when you need to work with both.
* Skip page furniture when you build your own text: type `header`/`footer`, or
  `getMetadata().get("content_layer")` equal to `"furniture"`. The `markdown`/`text`
  downloads already do.
* Metadata keys with a meaning (all added after 0.1.0): `content_layer="furniture"`,
  `markdown="blockquote"`, `list_ordered="true"`, `list_marker`, `code_language`, `recovered="text_layer"`
  (text the engine's Markdown dropped, restored from the PDF text layer). Unknown keys may appear later; ignore them.
* `Table#getCells()` is a `List<TableCellsInner>`: `getRow()`, `getCol()`, `getRowSpan()`,
  `getColSpan()`, `getText()`; `Table#getHeaderRows()`.
* `getWarnings()` codes explain degraded output; `getRouting()` says which engine ran.

Every field, the compatibility policy and the warning codes: [references/document-ir.md](references/document-ir.md).

## Errors

Failures throw `ApiException`; read `e.getCode()` (HTTP status) and, when the
server sent one, `e.getResponseBody()` for the stable `{"error":{"code":...}}`.

| status | code | retry | meaning |
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
| 429 | `rate_limited` | yes | honour `Retry-After` |
| 429 | `quota_exceeded` | **no** | the owner's rolling quota is spent |
| 503 | `coordinator_unavailable` | yes | the gateway cannot reach the coordinator |
| 507 | `insufficient_storage` | yes | server disk below its watermark |

The generated client has **no retry policy**: back off on the retryable rows and on
`500`/`502`/`504` (`Retry-After` when present), never on the others. A failed job carries
the same codes in `getErrorCode()`. Every code: [references/error-codes.md](references/error-codes.md).

## Notes on the model

* `Job.getStatus()` is `Job.StatusEnum`: `QUEUED`, `RUNNING`, `COMPLETED`, `FAILED`,
  `CANCELLED` (and `UNKNOWN_DEFAULT_OPEN_API` for a value this client does not know).
* `getRetentionStatus()`: `ACTIVE`, `RELEASE_PENDING`, `EXPIRY_PENDING`, `DELETING`,
  `RELEASED`, `EXPIRED`.
* `DocumentIR#getSchemaVersion()` is an enum (`DOC_IR_0_1`): a future breaking IR version
  fails at deserialisation instead of being misread.
* `DocumentIRRouting#getActualDevice()` is `cpu`, `gpu` or `unknown` (string, not
  an enum) — `unknown` means the execution device could not be proven.
* `mode`: `fast` (xberg), `accurate` (docling-rs; use it for multi-column PDFs), `auto`
  (router decides, may retry a bad scan once). Text formats always use the native engine.
* `device`: `auto` | `cpu` | `gpu_preferred` | `gpu_required`.

## Testing your integration

Run the lifecycle above against a real ParseDock instance (the server's `mock` engine
makes it fast and deterministic) and assert, at minimum: the job completes, both
downloads parse, an ACK with a wrong `result_version` returns `409`, and the job returns
`410` after a correct ACK. Do not stub the server: the ACK/TTL semantics are the part
worth testing.

## References (bundled with this skill)

* [references/document-ir.md](references/document-ir.md): every Document IR field.
* [references/error-codes.md](references/error-codes.md): every error code, HTTP status and retry rule.
* [references/openapi.yaml](references/openapi.yaml): the public HTTP API; the generated
  method and model names follow its `operationId`s and schema names.

Project and changelog: https://github.com/luxiang0412/ParseDock. Runnable example: [Java example](https://github.com/luxiang0412/ParseDock/tree/main/examples/java);
a JUnit integration test to copy: [Java client test](https://github.com/luxiang0412/ParseDock/tree/main/sdk/java-client-test).
