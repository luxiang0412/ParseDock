# InternalApi

All URIs are relative to *http://localhost:8080*

| Method | HTTP request | Description |
|------------- | ------------- | -------------|
| [**claimTask**](InternalApi.md#claimTask) | **POST** /internal/tasks/claim | Atomically lease one matching queued job |
| [**failTask**](InternalApi.md#failTask) | **POST** /internal/tasks/{id}/fail | Report a classified failure |
| [**fetchInput**](InternalApi.md#fetchInput) | **GET** /internal/tasks/{id}/input | Stream the input for a leased task |
| [**registerWorker**](InternalApi.md#registerWorker) | **POST** /internal/workers/register | Register worker capabilities |
| [**submitResult**](InternalApi.md#submitResult) | **PUT** /internal/tasks/{id}/result | Submit the Document IR (fenced commit) |
| [**taskHeartbeat**](InternalApi.md#taskHeartbeat) | **POST** /internal/tasks/{id}/heartbeat | Renew the lease and report progress |


<a id="claimTask"></a>
# **claimTask**
> ClaimedTask claimTask(claimTaskRequest)

Atomically lease one matching queued job

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.InternalApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure API key authorization: internalToken
    ApiKeyAuth internalToken = (ApiKeyAuth) defaultClient.getAuthentication("internalToken");
    internalToken.setApiKey("YOUR API KEY");
    // Uncomment the following line to set a prefix for the API key, e.g. "Token" (defaults to null)
    //internalToken.setApiKeyPrefix("Token");

    InternalApi apiInstance = new InternalApi(defaultClient);
    ClaimTaskRequest claimTaskRequest = new ClaimTaskRequest(); // ClaimTaskRequest | 
    try {
      ClaimedTask result = apiInstance.claimTask(claimTaskRequest);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling InternalApi#claimTask");
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
| **claimTaskRequest** | [**ClaimTaskRequest**](ClaimTaskRequest.md)|  | |

### Return type

[**ClaimedTask**](ClaimedTask.md)

### Authorization

[internalToken](../README.md#internalToken)

### HTTP request headers

 - **Content-Type**: application/json
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | A leased task. |  -  |
| **204** | Nothing to do. |  -  |

<a id="failTask"></a>
# **failTask**
> FailTask200Response failTask(id, failTaskRequest)

Report a classified failure

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.InternalApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure API key authorization: internalToken
    ApiKeyAuth internalToken = (ApiKeyAuth) defaultClient.getAuthentication("internalToken");
    internalToken.setApiKey("YOUR API KEY");
    // Uncomment the following line to set a prefix for the API key, e.g. "Token" (defaults to null)
    //internalToken.setApiKeyPrefix("Token");

    InternalApi apiInstance = new InternalApi(defaultClient);
    String id = "job_06GRQJT5HC1DDZMPWRSKSCKM24"; // String | 
    FailTaskRequest failTaskRequest = new FailTaskRequest(); // FailTaskRequest | 
    try {
      FailTask200Response result = apiInstance.failTask(id, failTaskRequest);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling InternalApi#failTask");
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
| **failTaskRequest** | [**FailTaskRequest**](FailTaskRequest.md)|  | |

### Return type

[**FailTask200Response**](FailTask200Response.md)

### Authorization

[internalToken](../README.md#internalToken)

### HTTP request headers

 - **Content-Type**: application/json
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | Failure recorded (job may have been requeued). |  -  |
| **409** | Lease lost. |  -  |

<a id="fetchInput"></a>
# **fetchInput**
> File fetchInput(id, xLeaseToken)

Stream the input for a leased task

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.InternalApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure API key authorization: internalToken
    ApiKeyAuth internalToken = (ApiKeyAuth) defaultClient.getAuthentication("internalToken");
    internalToken.setApiKey("YOUR API KEY");
    // Uncomment the following line to set a prefix for the API key, e.g. "Token" (defaults to null)
    //internalToken.setApiKeyPrefix("Token");

    InternalApi apiInstance = new InternalApi(defaultClient);
    String id = "job_06GRQJT5HC1DDZMPWRSKSCKM24"; // String | 
    String xLeaseToken = "xLeaseToken_example"; // String | 
    try {
      File result = apiInstance.fetchInput(id, xLeaseToken);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling InternalApi#fetchInput");
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
| **xLeaseToken** | **String**|  | |

### Return type

[**File**](File.md)

### Authorization

[internalToken](../README.md#internalToken)

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: application/octet-stream, application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | Raw input bytes. |  * X-Content-SHA256 -  <br>  |
| **409** | Lease invalid or expired. |  -  |

<a id="registerWorker"></a>
# **registerWorker**
> RegisterWorker200Response registerWorker(workerRegistration)

Register worker capabilities

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.InternalApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure API key authorization: internalToken
    ApiKeyAuth internalToken = (ApiKeyAuth) defaultClient.getAuthentication("internalToken");
    internalToken.setApiKey("YOUR API KEY");
    // Uncomment the following line to set a prefix for the API key, e.g. "Token" (defaults to null)
    //internalToken.setApiKeyPrefix("Token");

    InternalApi apiInstance = new InternalApi(defaultClient);
    WorkerRegistration workerRegistration = new WorkerRegistration(); // WorkerRegistration | 
    try {
      RegisterWorker200Response result = apiInstance.registerWorker(workerRegistration);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling InternalApi#registerWorker");
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
| **workerRegistration** | [**WorkerRegistration**](WorkerRegistration.md)|  | |

### Return type

[**RegisterWorker200Response**](RegisterWorker200Response.md)

### Authorization

[internalToken](../README.md#internalToken)

### HTTP request headers

 - **Content-Type**: application/json
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | Registered. |  -  |

<a id="submitResult"></a>
# **submitResult**
> SubmitResult200Response submitResult(id, submitResultRequest)

Submit the Document IR (fenced commit)

The coordinator renders Markdown/Text from the IR, writes artifacts atomically and commits the job only if the lease is still valid. A superseded attempt receives &#x60;409&#x60; and its files are discarded. 

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.InternalApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure API key authorization: internalToken
    ApiKeyAuth internalToken = (ApiKeyAuth) defaultClient.getAuthentication("internalToken");
    internalToken.setApiKey("YOUR API KEY");
    // Uncomment the following line to set a prefix for the API key, e.g. "Token" (defaults to null)
    //internalToken.setApiKeyPrefix("Token");

    InternalApi apiInstance = new InternalApi(defaultClient);
    String id = "job_06GRQJT5HC1DDZMPWRSKSCKM24"; // String | 
    SubmitResultRequest submitResultRequest = new SubmitResultRequest(); // SubmitResultRequest | 
    try {
      SubmitResult200Response result = apiInstance.submitResult(id, submitResultRequest);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling InternalApi#submitResult");
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
| **submitResultRequest** | [**SubmitResultRequest**](SubmitResultRequest.md)|  | |

### Return type

[**SubmitResult200Response**](SubmitResult200Response.md)

### Authorization

[internalToken](../README.md#internalToken)

### HTTP request headers

 - **Content-Type**: application/json
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | Result committed. |  -  |
| **409** | Lease expired/superseded — result rejected. |  -  |

<a id="taskHeartbeat"></a>
# **taskHeartbeat**
> TaskHeartbeat200Response taskHeartbeat(id, taskHeartbeatRequest)

Renew the lease and report progress

### Example
```java
// Import classes:
import org.openapitools.client.ApiClient;
import org.openapitools.client.ApiException;
import org.openapitools.client.Configuration;
import org.openapitools.client.auth.*;
import org.openapitools.client.models.*;
import org.openapitools.client.api.InternalApi;

public class Example {
  public static void main(String[] args) {
    ApiClient defaultClient = Configuration.getDefaultApiClient();
    defaultClient.setBasePath("http://localhost:8080");
    
    // Configure API key authorization: internalToken
    ApiKeyAuth internalToken = (ApiKeyAuth) defaultClient.getAuthentication("internalToken");
    internalToken.setApiKey("YOUR API KEY");
    // Uncomment the following line to set a prefix for the API key, e.g. "Token" (defaults to null)
    //internalToken.setApiKeyPrefix("Token");

    InternalApi apiInstance = new InternalApi(defaultClient);
    String id = "job_06GRQJT5HC1DDZMPWRSKSCKM24"; // String | 
    TaskHeartbeatRequest taskHeartbeatRequest = new TaskHeartbeatRequest(); // TaskHeartbeatRequest | 
    try {
      TaskHeartbeat200Response result = apiInstance.taskHeartbeat(id, taskHeartbeatRequest);
      System.out.println(result);
    } catch (ApiException e) {
      System.err.println("Exception when calling InternalApi#taskHeartbeat");
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
| **taskHeartbeatRequest** | [**TaskHeartbeatRequest**](TaskHeartbeatRequest.md)|  | |

### Return type

[**TaskHeartbeat200Response**](TaskHeartbeat200Response.md)

### Authorization

[internalToken](../README.md#internalToken)

### HTTP request headers

 - **Content-Type**: application/json
 - **Accept**: application/json

### HTTP response details
| Status code | Description | Response headers |
|-------------|-------------|------------------|
| **200** | Lease renewed. |  -  |
| **409** | Lease lost or the job was cancelled. |  -  |

