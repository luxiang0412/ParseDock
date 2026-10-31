---
name: parsedock-java-sdk
description: "ParseDock Java client (`com.luxiangs:parsedock-client`): upload a document with JobsApi, poll the job, download Markdown or the typed DocumentIR model, then ACK with the exact result_version to release the server copy. Use when: calling a ParseDock service from Java, Kotlin, Scala, Groovy or any JVM project; building an integration with Maven or Gradle; deserialising the Document IR (doc-ir/0.1) into generated model classes; handling ApiException codes such as rate_limited, quota_exceeded, file_too_large, encrypted_file or gpu_unavailable; or writing an end-to-end test against a running ParseDock instance. Also use when the user mentions ParseDock, JobsApi, DocumentIR, result_version, or ACK/TTL from a JVM context."
---

# ParseDock Java SDK

Generated from `api/openapi.yaml` with OpenAPI Generator (`make sdk`). The client
is checked in at `sdk/java-generated`; hand-written tests and examples live
outside it so regeneration cannot delete them.

Not on Maven Central yet — install the generated client into your local repo:

```bash
mvn -f sdk/java-generated/pom.xml -DskipTests install
```

```xml
<dependency>
  <groupId>com.luxiangs</groupId>
  <artifactId>parsedock-client</artifactId>
  <version>0.1.0</version>
</dependency>
```

Java 8+ compatible (CI builds and tests it on JDK 17).

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
JobsApi api = new JobsApi(client);

// createJob(File, mode, device, ttlSeconds, priority, idempotencyKey,
//           mode2, device2, ttlSeconds2, idempotencyKey2)
// The second set is the query-parameter mirror of the same fields: pass nulls.
Job job = api.createJob(new File("invoice.pdf"), "auto", "auto",
        null, null, null, null, null, null, null);

Job done;
do {
    Thread.sleep(300);
    done = api.getJob(job.getJobId());
} while (done.getStatus() == Job.StatusEnum.QUEUED
        || done.getStatus() == Job.StatusEnum.RUNNING);

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
  `X-Result-Version` header). A stale value → `409`, nothing released.
* Without an ACK the result expires at `result_expires_at` (TTL).
* `cancelJob(id, null)` only stops queued/running work: on a completed job it returns
  `409` and never releases the result.
* After release: `410` during the tombstone window, then `404`.
* `downloadResult` returns the body as a `String`; for large documents write it
  through `downloadResultWithHttpInfo`/streams rather than holding it in memory.

## Errors

Failures throw `ApiException`; read `e.getCode()` (HTTP status) and, when the
server sent one, `e.getResponseBody()` for the stable `{"error":{"code":...}}`.

| status | code | meaning |
| --- | --- | --- |
| 401 | `unauthorized` | bad or missing bearer token |
| 404 | `not_found` | unknown job, another owner's job, or already deleted |
| 409 | `conflict` | stale `result_version` on ACK, or ACK on a non-completed job |
| 410 | `gone` | released/expired (tombstone window) |
| 413 | `file_too_large` | over the deployment's upload cap |
| 415 | `unsupported_type` | format not enabled |
| 422 | `encrypted_file` | password-protected document |
| 429 | `rate_limited` | retry with backoff; honour `Retry-After` |
| 429 | `quota_exceeded` | do **not** retry: the owner's rolling quota is spent |
| 409 | `gpu_unavailable` | `gpu_required` without a proven GPU worker |

The generated client has no retry policy of its own: implement backoff for
`429 rate_limited`, `500`, `502`, `503`, `504` (`Retry-After` when present) and
never for the 4xx codes above.

## Notes on the model

* `Job.getStatus()` is `Job.StatusEnum` (`QUEUED`, `RUNNING`, `COMPLETED`, `FAILED`,
  `CANCELLED`); `getRetentionStatus()` is `ACTIVE` / `RELEASE_PENDING` / `RELEASED` / `EXPIRED`.
* `DocumentIR#getSchemaVersion()` is an enum (`DOC_IR_0_1`); a spec change that
  renames it will break compilation rather than at runtime — regenerate after
  pulling.
* `DocumentIRRouting#getActualDevice()` is `cpu`, `gpu` or `unknown` (string, not
  an enum) — `unknown` means the execution device could not be proven.
* `mode`: `fast` (xberg), `accurate` (docling-rs), `auto` (router decides, may
  retry once). Text formats always use the native engine.
* `device`: `auto` | `cpu` | `gpu_preferred` | `gpu_required`.

## Testing your integration

`sdk/java-client-test` is a Maven module with a JUnit test that runs the flow
above against a live server; copy it as a starting point.

```bash
mvn -f sdk/java-client-test/pom.xml test \
  -Dparsedock.base=http://localhost:8080 -Dparsedock.token=$PARSEDOCK_TOKEN \
  -Dparsedock.fixture=path/to/file.pdf
```

## More

`docs/SDK.md` (contract + testing matrix), `api/openapi.yaml` (every field),
`sdk/java-generated/README.md` (generated reference).
