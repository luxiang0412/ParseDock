# JobsApi

All URIs are relative to *http://localhost:8080*

| Method | HTTP request | Description |
|------------- | ------------- | -------------|
| [**ackJob**](JobsApi.md#ackJob) | **POST** /v1/jobs/{id}/ack | Accept that the client consumed the result, release storage asynchronously |
| [**cancelJob**](JobsApi.md#cancelJob) | **POST** /v1/jobs/{id}/cancel | Stop a queued/running job, or release a completed result |
| [**createJob**](JobsApi.md#createJob) | **POST** /v1/jobs | Upload a document and enqueue a parse job |
| [**downloadResult**](JobsApi.md#downloadResult) | **GET** /v1/jobs/{id}/result | Download a derived view of the committed result |
| [**getJob**](JobsApi.md#getJob) | **GET** /v1/jobs/{id} | Job status, retention state and result availability |


<a id="ackJob"></a>
# **ackJob**
> AckJob202Response ackJob(id, ackJobRequest, idempotencyKey)

Accept that the client consumed the result, release storage asynchronously

Idempotent. The first ACK must carry the &#x60;result_version&#x60; that was downloaded (mismatch → &#x60;409&#x60;, nothing is released). The response means \&quot;release intent accepted\&quot;; poll &#x60;GET /v1/jobs/{id}&#x60; until &#x60;retention_status&#x60; is &#x60;released&#x60;.  After a successful ACK, new downloads return &#x60;410&#x60; while in-flight downloads keep their lease. Repeat ACKs return &#x60;202&#x60; again and affect nothing else. 

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.JobsApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure HTTP bearer authorization: bearerAuth
    HttpBearerAuth bearerAuth = (HttpBearerAuth) defaultClient.getAuthentication("bearerAuth");
    bearerAuth.setBearerToken("BEARER TOKEN");

    JobsApi apiInstance = new JobsApi(defaultClient);
    String id = "job_06GRQJT5HC1DDZMPWRSKSCKM24"; // String | 
    AckJobRequest ackJobRequest = new AckJobRequest(); // AckJobRequest | 
    String idempotencyKey = "idempotencyKey_example"; // String | Same key + same owner returns the original job (`200`) instead of creating a second one.
    try {
      AckJob202Response result = apiInstance.ackJob(id, ackJobRequest, idempotencyKey);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling JobsApi#ackJob");
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
| **id** | **String**|  | |
| **ackJobRequest** | [**AckJobRequest**](AckJobRequest.md)|  | |
| **idempotencyKey** | **String**| Same key + same owner returns the original job (&#x60;200&#x60;) instead of creating a second one. | [optional] |

### Return type

[**AckJob202Response**](AckJob202Response.md)

### Authorization

[bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: application/json
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **202** | Release accepted (not necessarily deleted yet). |  -  |
| **400** | Invalid parameters. |  -  |
| **401** | Missing/invalid token. |  -  |
| **404** | Unknown job, or a job owned by somebody else. |  -  |
| **409** | result_version does not match the current result. |  -  |

<a id="cancelJob"></a>
# **cancelJob**
> CancelJob202Response cancelJob(id)

Stop a queued/running job, or release a completed result

Best effort and idempotent. A running job is marked &#x60;cancelled&#x60;; the worker notices on its next heartbeat and aborts. Input files are cleaned up by the normal retention path. 

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.JobsApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure HTTP bearer authorization: bearerAuth
    HttpBearerAuth bearerAuth = (HttpBearerAuth) defaultClient.getAuthentication("bearerAuth");
    bearerAuth.setBearerToken("BEARER TOKEN");

    JobsApi apiInstance = new JobsApi(defaultClient);
    String id = "job_06GRQJT5HC1DDZMPWRSKSCKM24"; // String | 
    try {
      CancelJob202Response result = apiInstance.cancelJob(id);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling JobsApi#cancelJob");
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
| **id** | **String**|  | |

### Return type

[**CancelJob202Response**](CancelJob202Response.md)

### Authorization

[bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **202** | Cancellation accepted. |  -  |
| **401** | Missing/invalid token. |  -  |
| **404** | Unknown job, or a job owned by somebody else. |  -  |

<a id="createJob"></a>
# **createJob**
> Job createJob(_file, mode, device, ttlSeconds, priority, idempotencyKey, mode2, device2, ttlSeconds2, idempotencyKey2)

Upload a document and enqueue a parse job

&#x60;multipart/form-data&#x60;. Put scalar fields **before** the &#x60;file&#x60; part; the implementation streams the file as soon as it encounters it (no full-body buffering). The same fields may instead be supplied as query parameters, which take precedence.  Upload limits are enforced server-side: size (&#x60;max_upload_mb&#x60;), page count (&#x60;max_pages&#x60;), archive expansion ratio, OOXML structure and PDF markers. The declared &#x60;Content-Type&#x60; of the file part is **not** trusted. 

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.JobsApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure HTTP bearer authorization: bearerAuth
    HttpBearerAuth bearerAuth = (HttpBearerAuth) defaultClient.getAuthentication("bearerAuth");
    bearerAuth.setBearerToken("BEARER TOKEN");

    JobsApi apiInstance = new JobsApi(defaultClient);
    File _file = new File("/path/to/file"); // File | The document. Required.
    String mode = "auto"; // String | `fast` prefers native text extraction; `accurate` requires a structured engine; `auto` lets the router choose (and records the decision). 
    String device = "auto"; // String | `gpu_required` is rejected with `409 gpu_unavailable` when no GPU worker is registered — it never silently runs on CPU. `gpu_preferred` may fall back to CPU, and the result marks the actual device. 
    Integer ttlSeconds = 56; // Integer | Requested result TTL, capped by the server's maximum.
    Integer priority = 0; // Integer | Higher runs first within the same queue.
    String idempotencyKey = "idempotencyKey_example"; // String | Same key + same owner returns the original job (`200`) instead of creating a second one.
    String mode2 = "auto"; // String | 
    String device2 = "auto"; // String | 
    Integer ttlSeconds2 = 56; // Integer | Requested result TTL; capped by the server maximum.
    String idempotencyKey2 = "idempotencyKey_example"; // String | Alternative to the Idempotency-Key header.
    try {
      Job result = apiInstance.createJob(_file, mode, device, ttlSeconds, priority, idempotencyKey, mode2, device2, ttlSeconds2, idempotencyKey2);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling JobsApi#createJob");
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
| **_file** | **File**| The document. Required. | |
| **mode** | **String**| &#x60;fast&#x60; prefers native text extraction; &#x60;accurate&#x60; requires a structured engine; &#x60;auto&#x60; lets the router choose (and records the decision).  | [optional] [default to auto] [enum: auto, fast, accurate] |
| **device** | **String**| &#x60;gpu_required&#x60; is rejected with &#x60;409 gpu_unavailable&#x60; when no GPU worker is registered — it never silently runs on CPU. &#x60;gpu_preferred&#x60; may fall back to CPU, and the result marks the actual device.  | [optional] [default to auto] [enum: auto, cpu, gpu_preferred, gpu_required] |
| **ttlSeconds** | **Integer**| Requested result TTL, capped by the server&#39;s maximum. | [optional] |
| **priority** | **Integer**| Higher runs first within the same queue. | [optional] [default to 0] |
| **idempotencyKey** | **String**| Same key + same owner returns the original job (&#x60;200&#x60;) instead of creating a second one. | [optional] |
| **mode2** | **String**|  | [optional] [default to auto] [enum: auto, fast, accurate] |
| **device2** | **String**|  | [optional] [default to auto] [enum: auto, cpu, gpu_preferred, gpu_required] |
| **ttlSeconds2** | **Integer**| Requested result TTL; capped by the server maximum. | [optional] |
| **idempotencyKey2** | **String**| Alternative to the Idempotency-Key header. | [optional] |

### Return type

[**Job**](Job.md)

### Authorization

[bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: multipart/form-data
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **202** | Job accepted and queued. |  * Location - Relative URL of the job resource. <br>  |
| **200** | Idempotent replay — the existing job is returned, nothing new was created. |  -  |
| **400** | Invalid parameters. |  -  |
| **401** | Missing/invalid token. |  -  |
| **409** | A GPU worker is required but none is registered, or the idempotency key conflicts. |  -  |
| **413** | Upload exceeds the configured limit. |  -  |
| **415** | Format not in the allow-list, or content does not match the extension. |  -  |
| **422** | Malformed, encrypted or structurally invalid document. |  -  |
| **429** | Per-owner rate limit exceeded. |  * Retry-After -  <br>  |
| **507** | Storage below the safety watermark; retry later (existing results stay readable). |  -  |

<a id="downloadResult"></a>
# **downloadResult**
> String downloadResult(id, format)

Download a derived view of the committed result

Streams the artifact and holds a **download lease** until the body is complete (renewed while streaming). An ACK during the stream does not truncate it; the cleaner waits for the lease to end before deleting.  Response headers: &#x60;ETag&#x60; and &#x60;X-Result-Version&#x60; carry the manifest hash, &#x60;Content-Length&#x60; is always set, &#x60;X-Download-Lease&#x60; is the lease id. Repeated downloads are allowed while &#x60;retention_status&#x3D;active&#x60;. 

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.JobsApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure HTTP bearer authorization: bearerAuth
    HttpBearerAuth bearerAuth = (HttpBearerAuth) defaultClient.getAuthentication("bearerAuth");
    bearerAuth.setBearerToken("BEARER TOKEN");

    JobsApi apiInstance = new JobsApi(defaultClient);
    String id = "job_06GRQJT5HC1DDZMPWRSKSCKM24"; // String | 
    String format = "markdown"; // String | `json` is the canonical Document IR (`doc-ir/0.1`); `markdown`/`text` are rendered from the IR. 
    try {
      String result = apiInstance.downloadResult(id, format);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling JobsApi#downloadResult");
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
| **id** | **String**|  | |
| **format** | **String**| &#x60;json&#x60; is the canonical Document IR (&#x60;doc-ir/0.1&#x60;); &#x60;markdown&#x60;/&#x60;text&#x60; are rendered from the IR.  | [optional] [default to markdown] [enum: markdown, text, json] |

### Return type

**String**

### Authorization

[bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: text/markdown, text/plain, application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | The requested representation. |  * ETag - \&quot;sha256:…\&quot; — identical to X-Result-Version. <br>  * X-Result-Version - Manifest hash the client must echo in the ACK. <br>  * X-Download-Lease -  <br>  * Content-Length -  <br>  |
| **401** | Missing/invalid token. |  -  |
| **404** | Unknown job, or a job owned by somebody else. |  -  |
| **409** | The result is not ready yet (job queued/running/failed). |  -  |
| **410** | The result was released or expired (tombstone window). |  -  |

<a id="getJob"></a>
# **getJob**
> Job getJob(id)

Job status, retention state and result availability

The authoritative view of a job. Use this instead of trusting the last SSE event: events are best-effort, the query is not. 

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.JobsApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure HTTP bearer authorization: bearerAuth
    HttpBearerAuth bearerAuth = (HttpBearerAuth) defaultClient.getAuthentication("bearerAuth");
    bearerAuth.setBearerToken("BEARER TOKEN");

    JobsApi apiInstance = new JobsApi(defaultClient);
    String id = "job_06GRQJT5HC1DDZMPWRSKSCKM24"; // String | 
    try {
      Job result = apiInstance.getJob(id);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling JobsApi#getJob");
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
| **id** | **String**|  | |

### Return type

[**Job**](Job.md)

### Authorization

[bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | Current job state. |  -  |
| **401** | Missing/invalid token. |  -  |
| **404** | Unknown job, or a job owned by somebody else. |  -  |
| **503** | The coordinator is unreachable (API replicas stay healthy). |  -  |

