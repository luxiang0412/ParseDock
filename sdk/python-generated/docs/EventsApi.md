# parsedock_client.EventsApi

All URIs are relative to *http://localhost:8080*

Method | HTTP request | Description
------------- | ------------- | -------------
[**job_events**](EventsApi.md#job_events) | **GET** /v1/jobs/{id}/events | Server-sent events for one job


# **job_events**
> str job_events(id, last_event_id=last_event_id, last_event_id2=last_event_id2)

Server-sent events for one job

`text/event-stream`. A `snapshot` event is sent first, then lifecycle
events (`created`, `running`, `retry_scheduled`, `completed`, `failed`,
`release_pending`, `lease_expired`, `released`, `expired`, `cancelled`).
Reconnect with `Last-Event-ID` (or `?last_event_id=`) to resume.
The stream ends with `stream_end` when the job reaches a terminal state;
the final answer is always `GET /v1/jobs/{id}`.


### Example

* Bearer Authentication (bearerAuth):

```python
import parsedock_client
from parsedock_client.rest import ApiException
from pprint import pprint

# Defining the host is optional and defaults to http://localhost:8080
# See configuration.py for a list of all supported configuration parameters.
configuration = parsedock_client.Configuration(
    host = "http://localhost:8080"
)

# The client must configure the authentication and authorization parameters
# in accordance with the API server security policy.
# Examples for each auth method are provided below, use the example that
# satisfies your auth use case.

# Configure Bearer authorization: bearerAuth
configuration = parsedock_client.Configuration(
    access_token = os.environ["BEARER_TOKEN"]
)

# Enter a context with an instance of the API client
with parsedock_client.ApiClient(configuration) as api_client:
    # Create an instance of the API class
    api_instance = parsedock_client.EventsApi(api_client)
    id = 'job_06GRQJT5HC1DDZMPWRSKSCKM24' # str | 
    last_event_id = 'last_event_id_example' # str |  (optional)
    last_event_id2 = 56 # int |  (optional)

    try:
        # Server-sent events for one job
        api_response = api_instance.job_events(id, last_event_id=last_event_id, last_event_id2=last_event_id2)
        print("The response of EventsApi->job_events:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling EventsApi->job_events: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **id** | **str**|  | 
 **last_event_id** | **str**|  | [optional] 
 **last_event_id2** | **int**|  | [optional] 

### Return type

**str**

### Authorization

[bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: text/event-stream, application/json

### HTTP response details

| Status code | Description | Response headers |
|-------------|-------------|------------------|
**200** | Event stream. |  -  |
**401** | Missing/invalid token. |  -  |
**404** | Unknown job, or a job owned by somebody else. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

