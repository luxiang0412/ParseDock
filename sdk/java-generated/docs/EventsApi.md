# EventsApi

All URIs are relative to *http://localhost:8080*

| Method | HTTP request | Description |
|------------- | ------------- | -------------|
| [**jobEvents**](EventsApi.md#jobEvents) | **GET** /v1/jobs/{id}/events | Server-sent events for one job |


<a id="jobEvents"></a>
# **jobEvents**
> String jobEvents(id, scope, lastEventID, lastEventId)

Server-sent events for one job

&#x60;text/event-stream&#x60;. A &#x60;snapshot&#x60; event is always sent first, then the lifecycle events.  Subscription scope: * &#x60;scope&#x3D;processing&#x60; (default) — events up to the terminal **processing**   status (&#x60;completed&#x60;/&#x60;failed&#x60;/&#x60;cancelled&#x60;); the stream then ends with   &#x60;stream_end&#x60;. Retention events are not delivered in this scope. * &#x60;scope&#x3D;retention&#x60; — continues into the retention lifecycle   (&#x60;release_pending&#x60;, &#x60;released&#x60;, &#x60;expired&#x60;) until it is terminal or the   stream hits its 10 minute cap (&#x60;stream_end&#x60; with   &#x60;reason&#x3D;max_duration&#x60;).  Reconnect with &#x60;Last-Event-ID&#x60; (or &#x60;?last_event_id&#x3D;&#x60;) to resume. Event rows are deleted with the job after the tombstone window (default 10 min), so a cursor older than the retained window receives a &#x60;cursor_reset&#x60; event followed by a fresh &#x60;snapshot&#x60; — never a silent gap. The final answer is always &#x60;GET /v1/jobs/{id}&#x60;. 

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
    String scope = "processing"; // String | How far into the lifecycle the stream follows the job.
    String lastEventID = "lastEventID_example"; // String | 
    Long lastEventId = 56L; // Long | 
    try {
      String result = apiInstance.jobEvents(id, scope, lastEventID, lastEventId);
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
| **scope** | **String**| How far into the lifecycle the stream follows the job. | [optional] [default to processing] [enum: processing, retention] |
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

