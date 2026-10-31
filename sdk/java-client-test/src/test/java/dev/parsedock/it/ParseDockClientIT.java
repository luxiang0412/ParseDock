package dev.parsedock.it;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotNull;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.io.File;
import java.util.concurrent.TimeUnit;

import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.MethodOrderer;
import org.junit.jupiter.api.Order;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.TestMethodOrder;
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.api.JobsApi;
import org.openapitools.client.model.AckJobRequest;
import org.openapitools.client.model.Job;

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
        String ir = api.downloadResult(jobId, "json");
        assertTrue(markdown != null && !markdown.isEmpty(), "empty markdown");
        assertTrue(ir.contains("\"schema_version\""), "IR download is not JSON IR");
        assertTrue(ir.contains("doc-ir/0.1"), "unexpected IR schema");
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
    void unknownJobIs404() {
        ApiException e = assertThrows(ApiException.class,
                () -> api.getJob("job_00000000000000000000000000"));
        assertEquals(404, e.getCode(), "unknown job must be 404: " + e.getMessage());
    }
}
