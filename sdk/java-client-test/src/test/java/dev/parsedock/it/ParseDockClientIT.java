package dev.parsedock.it;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.io.File;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.concurrent.TimeUnit;

import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.junit.jupiter.api.io.TempDir;
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.JSON;
import org.openapitools.client.api.JobsApi;
import org.openapitools.client.model.AckJobRequest;
import org.openapitools.client.model.DocumentIR;
import org.openapitools.client.model.DocumentIRRouting;
import org.openapitools.client.model.DocumentIRWarningsInner;
import org.openapitools.client.model.Element;
import org.openapitools.client.model.Job;
import org.openapitools.client.model.Table;

/**
 * The generated Java client against a live ParseDock stack.
 *
 * <p>The stack, its token and the fixture path come from system properties so the
 * test owns no infrastructure of its own: {@code scripts/sdk-check.sh} starts a
 * standalone process (mock engine) and passes
 * {@code -Dparsedock.base/-Dparsedock.token/-Dparsedock.fixture}.
 *
 * <p>What this proves that a compile check cannot: the generated client can
 * authenticate, upload multipart, poll, download both representations, ACK with
 * the version it downloaded, and see the contract enforced (stale version 409,
 * download after ACK 410, unknown job 404).
 */
@TestMethodOrder(MethodOrderer.OrderAnnotation.class)
class ParseDockClientIT {

    private static JobsApi api;
    private static File fixture;
    private static String jobId;
    private static String resultVersion;

    @BeforeAll
    static void connect() {
        String base = System.getProperty("parsedock.base", "");
        String token = System.getProperty("parsedock.token", "");
        String fixturePath = System.getProperty("parsedock.fixture", "");
        assertFalse(base.isEmpty(), "sdk-check.sh must pass -Dparsedock.base");
        assertFalse(token.isEmpty(), "sdk-check.sh must pass -Dparsedock.token");
        fixture = new File(fixturePath);
        assertTrue(fixture.isFile(), "fixture not found: " + fixturePath);

        ApiClient client = new ApiClient();
        client.setBasePath(base);
        client.setBearerToken(token);
        api = new JobsApi(client);
    }

    @Test
    @Order(1)
    void createsAndCompletesAJob() throws Exception {
        Job created = api.createJob(fixture, "fast", "auto", null, null, null,
                null, null, null, null);
        assertNotNull(created.getJobId(), "no job id returned");
        assertEquals("queued", created.getStatus().getValue(), "status right after upload");
        jobId = created.getJobId();

        Job job = created;
        long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(120);
        while (job.getStatus().getValue().equals("queued")
                || job.getStatus().getValue().equals("running")) {
            assertTrue(System.nanoTime() < deadline, "job did not finish in 120s");
            Thread.sleep(300);
            job = api.getJob(jobId);
        }
        assertEquals("completed", job.getStatus().getValue(),
                "job failed: " + job.getErrorCode() + " " + job.getErrorMessage());
        assertNotNull(job.getEngine(), "engine not recorded");
        resultVersion = job.getResultVersion();
        assertNotNull(resultVersion, "result_version missing");
    }

    @Test
    @Order(2)
    void downloadsBothRepresentations() throws Exception {
        String markdown = api.downloadResult(jobId, "markdown");
        String irJson = api.downloadResult(jobId, "json");
        assertTrue(markdown != null && !markdown.isEmpty(), "empty markdown");
        assertTrue(irJson != null && irJson.contains("\"schema_version\""), "IR download is not JSON IR");

        // Deserialise with the generated model, exactly as a consumer would: a field
        // the client cannot bind is a broken contract, not a formatting detail.
        DocumentIR ir = JSON.deserialize(irJson, DocumentIR.class);
        assertNotNull(ir, "IR did not deserialise into the generated model");
        assertEquals(DocumentIR.SchemaVersionEnum.DOC_IR_0_1, ir.getSchemaVersion(),
                "schema version drifted from the client");
        assertNotNull(ir.getParserEngine(), "parser_engine missing");
        assertNotNull(ir.getElements(), "elements missing");
        assertFalse(ir.getElements().isEmpty(), "no elements in the IR");
        for (Element el : ir.getElements()) {
            assertNotNull(el.getType(), "element without a type");
            assertNotNull(el.getId(), "element without an id");
        }

        DocumentIRRouting routing = ir.getRouting();
        assertNotNull(routing, "routing missing");
        assertNotNull(routing.getActualDevice(), "actual_device missing");
        assertTrue(routing.getActualDevice().equals("cpu")
                        || routing.getActualDevice().equals("gpu")
                        || routing.getActualDevice().equals("unknown"),
                "unexpected actual_device: " + routing.getActualDevice());
        assertNotNull(routing.getEngine(), "routing.engine missing");

        for (DocumentIRWarningsInner w : ir.getWarnings()) {
            assertNotNull(w.getCode(), "warning without a code");
            assertFalse(w.getCode().isEmpty(), "empty warning code");
        }
        for (Table t : ir.getTables()) {
            assertTrue(t.getRows() > 0 && t.getCols() > 0, "table without a grid");
            assertFalse(t.getCells() == null || t.getCells().isEmpty(), "table without cells");
            assertTrue(t.getCells().size() <= t.getRows() * t.getCols(),
                    "more cells than the grid can hold");
        }
    }

    @Test
    @Order(3)
    void refusesAStaleAckVersion() {
        ApiException e = assertThrows(ApiException.class,
                () -> api.ackJob(jobId, new AckJobRequest().resultVersion("sha256:deadbeef"), null));
        assertEquals(409, e.getCode(), "stale result_version must be refused: " + e.getMessage());
    }

    @Test
    @Order(4)
    void acksAndTheResultDisappears() throws Exception {
        api.ackJob(jobId, new AckJobRequest().resultVersion(resultVersion), null);

        ApiException e = assertThrows(ApiException.class, () -> api.downloadResult(jobId, "markdown"));
        assertEquals(410, e.getCode(), "download after ACK must be 410: " + e.getMessage());
    }

    @Test
    @Order(5)
    void cancelOnACompletedJobIsRefused() throws Exception {
        // Cancelling finished work must not release it: that is ACK's job, and a
        // second release path would make the outcome depend on a race. Uses its own
        // job so the ACK assertions above keep their meaning.
        Job job = api.createJob(fixture, "fast", "auto", null, null, null,
                null, null, null, null);
        Job done = job;
        long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(120);
        while (done.getStatus() == Job.StatusEnum.QUEUED || done.getStatus() == Job.StatusEnum.RUNNING) {
            assertTrue(System.nanoTime() < deadline, "job did not finish in 120s");
            Thread.sleep(300);
            done = api.getJob(job.getJobId());
        }
        assertEquals("completed", done.getStatus().getValue(), "job failed before the cancel check");

        final String cancelTarget = done.getJobId();
        ApiException e = assertThrows(ApiException.class,
                () -> api.cancelJob(cancelTarget, null));
        assertEquals(409, e.getCode(), "cancel on a completed job must be 409: " + e.getMessage());

        // The result is still there: only ACK releases it.
        assertNotNull(api.downloadResult(done.getJobId(), "markdown"));
        api.ackJob(done.getJobId(),
                new AckJobRequest().resultVersion(done.getResultVersion()), null);
    }

    @Test
    @Order(7)
    void uploadsNonAsciiAndQuotedFileNames(@TempDir Path tmp) throws Exception {
        // Issue #66: OkHttp rejects non-ASCII header values, and a '"' used to end the
        // filename parameter. Both must round-trip as input_name, or CJK/Portuguese
        // document names are unusable from this client.
        String[] names = {"plain name.pdf", "café.pdf", "测试-合同.pdf", "a\"b.pdf"};
        for (String name : names) {
            Path target = tmp.resolve(name);
            Files.copy(fixture.toPath(), target, StandardCopyOption.REPLACE_EXISTING);

            Job created = api.createJob(target.toFile(), "fast", "auto",
                    null, null, null, null, null, null, null);
            assertEquals(name, created.getInputName(), "input_name changed for " + name);

            Job job = created;
            long deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(120);
            while (job.getStatus().getValue().equals("queued")
                    || job.getStatus().getValue().equals("running")) {
                assertTrue(System.nanoTime() < deadline,
                        "job for " + name + " did not finish in 120s");
                Thread.sleep(300);
                job = api.getJob(job.getJobId());
            }
            assertEquals("completed", job.getStatus().getValue(),
                    "job for " + name + " failed: " + job.getErrorCode() + " "
                            + job.getErrorMessage());
            api.ackJob(job.getJobId(),
                    new AckJobRequest().resultVersion(job.getResultVersion()), null);
        }
    }

    @Test
    @Order(6)
    void unknownJobIs404() {
        ApiException e = assertThrows(ApiException.class,
                () -> api.getJob("job_00000000000000000000000000"));
        assertEquals(404, e.getCode(), "unknown job must be 404: " + e.getMessage());
    }
}
