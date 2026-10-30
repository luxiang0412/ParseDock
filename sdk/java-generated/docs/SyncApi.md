# SyncApi

All URIs are relative to *http://localhost:8080*

| Method | HTTP request | Description |
|------------- | ------------- | -------------|
| [**parseSync**](SyncApi.md#parseSync) | **POST** /v1/parse | Bounded synchronous parse (small documents) |


<a id="parseSync"></a>
# **parseSync**
> DocumentIR parseSync(_file, mode, device, timeoutMs)

Bounded synchronous parse (small documents)

Same validation and pipeline as &#x60;POST /v1/jobs&#x60;, but waits up to &#x60;timeout_ms&#x60; (default 30 s, max 2 min) for completion. * finished → &#x60;200&#x60; with the Document IR and the result is released immediately   (the body was fully written, so no retention window is needed); * deadline reached → &#x60;202&#x60; with the job resource; it keeps running asynchronously; * failure → &#x60;422&#x60; with the stable error code. Intended for interactive callers; prefer &#x60;/v1/jobs&#x60; for anything large. 

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
    
    // Configure HTTP bearer authorization: bearerAuth
    HttpBearerAuth bearerAuth = (HttpBearerAuth) defaultClient.getAuthentication("bearerAuth");
    bearerAuth.setBearerToken("BEARER TOKEN");

    SyncApi apiInstance = new SyncApi(defaultClient);
    File _file = new File("/path/to/file"); // File | 
    String mode = "auto"; // String | `fast` prefers native text extraction; `accurate` requires a structured engine; `auto` lets the router choose (and records the decision). 
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
| **mode** | **String**| &#x60;fast&#x60; prefers native text extraction; &#x60;accurate&#x60; requires a structured engine; &#x60;auto&#x60; lets the router choose (and records the decision).  | [optional] [default to auto] [enum: auto, fast, accurate] |
| **device** | **String**| &#x60;gpu_required&#x60; is rejected with &#x60;409 gpu_unavailable&#x60; when no GPU worker is registered — it never silently runs on CPU. &#x60;gpu_preferred&#x60; may fall back to CPU, and the result marks the actual device.  | [optional] [default to auto] [enum: auto, cpu, gpu_preferred, gpu_required] |
| **timeoutMs** | **Integer**|  | [optional] [default to 30000] |

### Return type

[**DocumentIR**](DocumentIR.md)

### Authorization

[bearerAuth](../README.md#bearerAuth)

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
| **413** | Upload exceeds the configured limit. |  -  |
| **422** | Parse failed or the document is unsupported. |  -  |

