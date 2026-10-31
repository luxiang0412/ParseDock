# SyncApi

All URIs are relative to *http://localhost:8080*

| Method | HTTP request | Description |
|------------- | ------------- | -------------|
| [**parseSync**](SyncApi.md#parseSync) | **POST** /v1/parse | Bounded synchronous parse (small documents) |


<a id="parseSync"></a>
# **parseSync**
> DocumentIR parseSync(_file, mode, device, timeoutMs)

Bounded synchronous parse (small documents)

Same validation and pipeline as &#x60;POST /v1/jobs&#x60;, but waits up to &#x60;timeout_ms&#x60; (default 30 s, max 2 min) for completion. * finished → &#x60;200&#x60; with the Document IR; * deadline reached → &#x60;202&#x60; with the job resource; it keeps running asynchronously; * failure → &#x60;422&#x60; with the stable error code.  The result is **not released** by this call: a written response does not prove the caller received it, so the normal TTL applies. The response carries &#x60;X-Job-Id&#x60; and &#x60;X-Result-Version&#x60;; release with &#x60;POST /v1/jobs/{id}/ack&#x60; when your sink has the bytes. Intended for interactive callers; prefer &#x60;/v1/jobs&#x60; for anything large. 

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.SyncApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure API key authorization: apiKeyAuth
    ApiKeyAuth apiKeyAuth = (ApiKeyAuth) defaultClient.getAuthentication("apiKeyAuth");
    apiKeyAuth.setApiKey("YOUR API KEY");
    // Uncomment the following line to set a prefix for the API key, e.g. "Token" (defaults to null)
    //apiKeyAuth.setApiKeyPrefix("Token");

    // Configure HTTP bearer authorization: bearerAuth
    HttpBearerAuth bearerAuth = (HttpBearerAuth) defaultClient.getAuthentication("bearerAuth");
    bearerAuth.setBearerToken("BEARER TOKEN");

    SyncApi apiInstance = new SyncApi(defaultClient);
    File _file = new File("/path/to/file"); // File | 
    String mode = "auto"; // String | A **policy preference, not a per-document accuracy guarantee**:  * `fast` — fastest engine that can read the format (xberg for PDF/Office). * `accurate` — requires an engine that declares structured output   (docling-rs: reading order, bbox, table spans, sheet names). * `auto` — router picks by priority and records why.  Text formats (`txt`, `md`, `csv`, `tsv`) always use the native engine in every mode: it is orders of magnitude faster and decodes GB18030/UTF-16 correctly, which the ML engines do not. Measured counter-examples where `accurate` is *not* more accurate (OCR-heavy scans, DOCX/XLSX text) are documented in `docs/ENGINE-BENCHMARK.md`; the engine, version and reason are always recorded in the job and the IR. 
    String device = "auto"; // String | `gpu_required` is rejected with `409 gpu_unavailable` when no GPU worker is registered — it never silently runs on CPU. `gpu_preferred` may fall back to CPU, and the result marks the actual device. 
    Integer timeoutMs = 30000; // Integer | 
    try {
      DocumentIR result = apiInstance.parseSync(_file, mode, device, timeoutMs);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling SyncApi#parseSync");
      System.err.println("Status code: " + e.getCode());
      System.err.println("Reason: " + e.getResponseBody());
      System.err.println("Response headers: " + e.getResponseHeaders());
      e.printStackTrace();
    }
  }
}
```

### Parameters

| Name | Type | Description  | Notes |
|------------- | ------------- | ------------- | -------------|
| **_file** | **File**|  | |
| **mode** | **String**| A **policy preference, not a per-document accuracy guarantee**:  * &#x60;fast&#x60; — fastest engine that can read the format (xberg for PDF/Office). * &#x60;accurate&#x60; — requires an engine that declares structured output   (docling-rs: reading order, bbox, table spans, sheet names). * &#x60;auto&#x60; — router picks by priority and records why.  Text formats (&#x60;txt&#x60;, &#x60;md&#x60;, &#x60;csv&#x60;, &#x60;tsv&#x60;) always use the native engine in every mode: it is orders of magnitude faster and decodes GB18030/UTF-16 correctly, which the ML engines do not. Measured counter-examples where &#x60;accurate&#x60; is *not* more accurate (OCR-heavy scans, DOCX/XLSX text) are documented in &#x60;docs/ENGINE-BENCHMARK.md&#x60;; the engine, version and reason are always recorded in the job and the IR.  | [optional] [default to auto] [enum: auto, fast, accurate] |
| **device** | **String**| &#x60;gpu_required&#x60; is rejected with &#x60;409 gpu_unavailable&#x60; when no GPU worker is registered — it never silently runs on CPU. &#x60;gpu_preferred&#x60; may fall back to CPU, and the result marks the actual device.  | [optional] [default to auto] [enum: auto, cpu, gpu_preferred, gpu_required] |
| **timeoutMs** | **Integer**|  | [optional] [default to 30000] |

### Return type

[**DocumentIR**](DocumentIR.md)

### Authorization

[apiKeyAuth](../README.md#apiKeyAuth), [bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: multipart/form-data
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | Parsed document (IR). |  -  |
| **202** | Still running; the job continues in the background. |  -  |
| **400** | Invalid parameters. |  -  |
| **401** | Missing/invalid token. |  -  |
| **403** | The API key is valid but does not grant the scope this operation needs. |  -  |
| **413** | Upload exceeds the configured limit. |  -  |
| **422** | Parse failed or the document is unsupported. |  -  |
| **429** | Per-owner limits. &#x60;rate_limited&#x60; &#x3D; request-rate budget (all requests, including status polling and event streams); &#x60;quota_exceeded&#x60; &#x3D; concurrent jobs, jobs/day or bytes/day for this owner. Both carry &#x60;Retry-After&#x60; in seconds. Retrying &#x60;rate_limited&#x60; after that delay is expected; retrying &#x60;quota_exceeded&#x60; is not - the quota only resets when its window passes.  |  * Retry-After -  <br>  |

