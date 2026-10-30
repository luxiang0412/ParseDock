# ProbesApi

All URIs are relative to *http://localhost:8080*

| Method | HTTP request | Description |
|------------- | ------------- | -------------|
| [**healthz**](ProbesApi.md#healthz) | **GET** /healthz | Liveness |
| [**readyz**](ProbesApi.md#readyz) | **GET** /readyz | Readiness (delegated to the coordinator) |


<a id="healthz"></a>
# **healthz**
> Healthz200Response healthz()

Liveness

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.models.*;
import org.openapitools.client.api.ProbesApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");

    ProbesApi apiInstance = new ProbesApi(defaultClient);
    try {
      Healthz200Response result = apiInstance.healthz();
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling ProbesApi#healthz");
      System.err.println("Status code: " + e.getCode());
      System.err.println("Reason: " + e.getResponseBody());
      System.err.println("Response headers: " + e.getResponseHeaders());
      e.printStackTrace();
    }
  }
}
```

### Parameters
This endpoint does not need any parameter.

### Return type

[**Healthz200Response**](Healthz200Response.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | Process is serving. |  -  |

<a id="readyz"></a>
# **readyz**
> Readiness readyz()

Readiness (delegated to the coordinator)

Returns &#x60;503&#x60; when SQLite is down or the cleaner has stalled. A low disk watermark keeps the service &#x60;ready&#x60; (reads and ACKs must keep working) but reports &#x60;accepting_uploads&#x3D;false&#x60;. 

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.models.*;
import org.openapitools.client.api.ProbesApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");

    ProbesApi apiInstance = new ProbesApi(defaultClient);
    try {
      Readiness result = apiInstance.readyz();
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling ProbesApi#readyz");
      System.err.println("Status code: " + e.getCode());
      System.err.println("Reason: " + e.getResponseBody());
      System.err.println("Response headers: " + e.getResponseHeaders());
      e.printStackTrace();
    }
  }
}
```

### Parameters
This endpoint does not need any parameter.

### Return type

[**Readiness**](Readiness.md)

### Authorization

No authorization required

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | Ready. |  -  |
| **503** | Not ready. |  -  |

