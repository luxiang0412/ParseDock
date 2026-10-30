# EventsApi

All URIs are relative to *http://localhost:8080*

| Method | HTTP request | Description |
|------------- | ------------- | -------------|
| [**jobEvents**](EventsApi.md#jobEvents) | **GET** /v1/jobs/{id}/events | Server-sent events for one job |


<a id="jobEvents"></a>
# **jobEvents**
> String jobEvents(id, lastEventID, lastEventId)

Server-sent events for one job

&#x60;text/event-stream&#x60;. A &#x60;snapshot&#x60; event is sent first, then lifecycle events (&#x60;created&#x60;, &#x60;running&#x60;, &#x60;retry_scheduled&#x60;, &#x60;completed&#x60;, &#x60;failed&#x60;, &#x60;release_pending&#x60;, &#x60;lease_expired&#x60;, &#x60;released&#x60;, &#x60;expired&#x60;, &#x60;cancelled&#x60;). Reconnect with &#x60;Last-Event-ID&#x60; (or &#x60;?last_event_id&#x3D;&#x60;) to resume. The stream ends with &#x60;stream_end&#x60; when the job reaches a terminal state; the final answer is always &#x60;GET /v1/jobs/{id}&#x60;. 

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.EventsApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure HTTP bearer authorization: bearerAuth
    HttpBearerAuth bearerAuth = (HttpBearerAuth) defaultClient.getAuthentication("bearerAuth");
    bearerAuth.setBearerToken("BEARER TOKEN");

    EventsApi apiInstance = new EventsApi(defaultClient);
    String id = "job_06GRQJT5HC1DDZMPWRSKSCKM24"; // String | 
    String lastEventID = "lastEventID_example"; // String | 
    Long lastEventId = 56L; // Long | 
    try {
      String result = apiInstance.jobEvents(id, lastEventID, lastEventId);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling EventsApi#jobEvents");
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
| **lastEventID** | **String**|  | [optional] |
| **lastEventId** | **Long**|  | [optional] |

### Return type

**String**

### Authorization

[bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: text/event-stream, application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | Event stream. |  -  |
| **401** | Missing/invalid token. |  -  |
| **404** | Unknown job, or a job owned by somebody else. |  -  |

