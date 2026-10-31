package dev.parsedock.example;

import java.io.File;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;

import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.api.JobsApi;
import org.openapitools.client.model.AckJobRequest;
import org.openapitools.client.model.Job;

/**
 * Minimal ParseDock example: parse one document and store the result.
 *
 * <p>The server copy is released (ACKed) only after the bytes are written, and
 * the ACK carries the exact {@code result_version} that was downloaded.
 *
 * <pre>
 *   export PARSEDOCK_TOKEN=...
 *   mvn -q compile exec:java -Dexec.args="invoice.pdf"
 * </pre>
 */
public final class ParseFile {

    private ParseFile() {
    }

    public static void main(String[] args) throws Exception {
        String api = env("PARSEDOCK_API", "http://127.0.0.1:8080");
        String token = env("PARSEDOCK_TOKEN", "");
        String mode = env("PARSEDOCK_MODE", "auto");
        String format = env("PARSEDOCK_FORMAT", "markdown");
        String input = args.length > 0 ? args[0] : "benchmark/fixtures/generated/pdf_text_en_3p.pdf";

        if (token.isEmpty()) {
            System.err.println("set PARSEDOCK_TOKEN (key from deploy/.env, PARSEDOCK_API_KEYS)");
            System.exit(2);
        }
        File file = new File(input);
        if (!file.isFile()) {
            System.err.println("no such file: " + file.getAbsolutePath());
            System.exit(2);
        }

        ApiClient client = new ApiClient();
        client.setBasePath(api);
        client.setBearerToken(token);
        JobsApi jobs = new JobsApi(client);

        try {
            // createJob(file, mode, device, ttlSeconds, priority, idempotencyKey,
            //           + the query mirror of the same fields: nulls)
            // No idempotency key: each run is a new job. Pass one (in the 6th
            // argument) when a retry of the *same* upload must not create a second
            // job; replaying a key returns the original job, whose result may
            // already have been released by an earlier ACK.
            Job job = jobs.createJob(file, mode, "auto", null, null, null,
                    null, null, null, null);
            System.out.println("job " + job.getJobId() + " queued; waiting…");

            Job done = job;
            long deadline = System.nanoTime() + 600_000_000_000L; // 10 min
            while (done.getStatus() == Job.StatusEnum.QUEUED
                    || done.getStatus() == Job.StatusEnum.RUNNING) {
                if (System.nanoTime() > deadline) {
                    System.err.println("timed out waiting for " + job.getJobId());
                    System.exit(1);
                }
                Thread.sleep(300);
                done = jobs.getJob(job.getJobId());
            }
            if (done.getStatus() != Job.StatusEnum.COMPLETED) {
                System.err.println("parse failed: " + done.getErrorCode() + " "
                        + (done.getErrorMessage() == null ? "" : done.getErrorMessage()));
                System.exit(1);
            }

            String body = jobs.downloadResult(done.getJobId(), format);
            Path out = outputPath(file, format);
            Files.createDirectories(out.getParent());
            Files.write(out, body.getBytes(StandardCharsets.UTF_8));   // store first…
            System.out.println(done.getEngine() + " · " + body.length() + " chars · "
                    + done.getResultVersion() + " → " + out);

            if (!env("PARSEDOCK_KEEP", "").isEmpty()) {
                System.out.println("kept on server; ACK it later with " + done.getResultVersion());
                return;
            }
            jobs.ackJob(done.getJobId(),                          // …release only after the write
                    new AckJobRequest().resultVersion(done.getResultVersion()), null);
            System.out.println("released (ACK sent after the file was written)");
        } catch (ApiException e) {
            System.err.println("server error " + e.getCode() + ": " + e.getMessage());
            System.exit(1);
        }
    }

    private static Path outputPath(File input, String format) {
        String ext = format.equals("json") ? ".json" : format.equals("text") ? ".txt" : ".md";
        return Paths.get("out", input.getName() + ext);
    }

    private static String env(String name, String fallback) {
        String v = System.getenv(name);
        return v == null || v.isEmpty() ? fallback : v;
    }
}
