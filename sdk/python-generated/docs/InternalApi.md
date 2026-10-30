# parsedock_client.InternalApi

All URIs are relative to *http://localhost:8080*

Method | HTTP request | Description
------------- | ------------- | -------------
[**claim_task**](InternalApi.md#claim_task) | **POST** /internal/tasks/claim | Atomically lease one matching queued job
[**fail_task**](InternalApi.md#fail_task) | **POST** /internal/tasks/{id}/fail | Report a classified failure
[**fetch_input**](InternalApi.md#fetch_input) | **GET** /internal/tasks/{id}/input | Stream the input for a leased task
[**register_worker**](InternalApi.md#register_worker) | **POST** /internal/workers/register | Register worker capabilities
[**submit_result**](InternalApi.md#submit_result) | **PUT** /internal/tasks/{id}/result | Submit the Document IR (fenced commit)
[**task_heartbeat**](InternalApi.md#task_heartbeat) | **POST** /internal/tasks/{id}/heartbeat | Renew the lease and report progress


# **claim_task**
> ClaimedTask claim_task(claim_task_request)

Atomically lease one matching queued job

### Example

* Api Key Authentication (internalToken):

```python
import parsedock_client
from parsedock_client.models.claim_task_request import ClaimTaskRequest
from parsedock_client.models.claimed_task import ClaimedTask
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

# Configure API key authorization: internalToken
configuration.api_key['internalToken'] = os.environ["API_KEY"]

# Uncomment below to setup prefix (e.g. Bearer) for API key, if needed
# configuration.api_key_prefix['internalToken'] = 'Bearer'

# Enter a context with an instance of the API client
with parsedock_client.ApiClient(configuration) as api_client:
    # Create an instance of the API class
    api_instance = parsedock_client.InternalApi(api_client)
    claim_task_request = parsedock_client.ClaimTaskRequest() # ClaimTaskRequest | 

    try:
        # Atomically lease one matching queued job
        api_response = api_instance.claim_task(claim_task_request)
        print("The response of InternalApi->claim_task:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling InternalApi->claim_task: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **claim_task_request** | [**ClaimTaskRequest**](ClaimTaskRequest.md)|  | 

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
**200** | A leased task. |  -  |
**204** | Nothing to do. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **fail_task**
> FailTask200Response fail_task(id, fail_task_request)

Report a classified failure

### Example

* Api Key Authentication (internalToken):

```python
import parsedock_client
from parsedock_client.models.fail_task200_response import FailTask200Response
from parsedock_client.models.fail_task_request import FailTaskRequest
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

# Configure API key authorization: internalToken
configuration.api_key['internalToken'] = os.environ["API_KEY"]

# Uncomment below to setup prefix (e.g. Bearer) for API key, if needed
# configuration.api_key_prefix['internalToken'] = 'Bearer'

# Enter a context with an instance of the API client
with parsedock_client.ApiClient(configuration) as api_client:
    # Create an instance of the API class
    api_instance = parsedock_client.InternalApi(api_client)
    id = 'job_06GRQJT5HC1DDZMPWRSKSCKM24' # str | 
    fail_task_request = parsedock_client.FailTaskRequest() # FailTaskRequest | 

    try:
        # Report a classified failure
        api_response = api_instance.fail_task(id, fail_task_request)
        print("The response of InternalApi->fail_task:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling InternalApi->fail_task: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **id** | **str**|  | 
 **fail_task_request** | [**FailTaskRequest**](FailTaskRequest.md)|  | 

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
**200** | Failure recorded (job may have been requeued). |  -  |
**409** | Lease lost. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **fetch_input**
> bytes fetch_input(id, x_lease_token)

Stream the input for a leased task

### Example

* Api Key Authentication (internalToken):

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

# Configure API key authorization: internalToken
configuration.api_key['internalToken'] = os.environ["API_KEY"]

# Uncomment below to setup prefix (e.g. Bearer) for API key, if needed
# configuration.api_key_prefix['internalToken'] = 'Bearer'

# Enter a context with an instance of the API client
with parsedock_client.ApiClient(configuration) as api_client:
    # Create an instance of the API class
    api_instance = parsedock_client.InternalApi(api_client)
    id = 'job_06GRQJT5HC1DDZMPWRSKSCKM24' # str | 
    x_lease_token = 'x_lease_token_example' # str | 

    try:
        # Stream the input for a leased task
        api_response = api_instance.fetch_input(id, x_lease_token)
        print("The response of InternalApi->fetch_input:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling InternalApi->fetch_input: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **id** | **str**|  | 
 **x_lease_token** | **str**|  | 

### Return type

**bytes**

### Authorization

[internalToken](../README.md#internalToken)

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: application/octet-stream, application/json

### HTTP response details

| Status code | Description | Response headers |
|-------------|-------------|------------------|
**200** | Raw input bytes. |  * X-Content-SHA256 -  <br>  |
**409** | Lease invalid or expired. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **register_worker**
> RegisterWorker200Response register_worker(worker_registration)

Register worker capabilities

### Example

* Api Key Authentication (internalToken):

```python
import parsedock_client
from parsedock_client.models.register_worker200_response import RegisterWorker200Response
from parsedock_client.models.worker_registration import WorkerRegistration
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

# Configure API key authorization: internalToken
configuration.api_key['internalToken'] = os.environ["API_KEY"]

# Uncomment below to setup prefix (e.g. Bearer) for API key, if needed
# configuration.api_key_prefix['internalToken'] = 'Bearer'

# Enter a context with an instance of the API client
with parsedock_client.ApiClient(configuration) as api_client:
    # Create an instance of the API class
    api_instance = parsedock_client.InternalApi(api_client)
    worker_registration = parsedock_client.WorkerRegistration() # WorkerRegistration | 

    try:
        # Register worker capabilities
        api_response = api_instance.register_worker(worker_registration)
        print("The response of InternalApi->register_worker:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling InternalApi->register_worker: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **worker_registration** | [**WorkerRegistration**](WorkerRegistration.md)|  | 

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
**200** | Registered. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **submit_result**
> SubmitResult200Response submit_result(id, submit_result_request)

Submit the Document IR (fenced commit)

The coordinator renders Markdown/Text from the IR, writes artifacts
atomically and commits the job only if the lease is still valid. A
superseded attempt receives `409` and its files are discarded.


### Example

* Api Key Authentication (internalToken):

```python
import parsedock_client
from parsedock_client.models.submit_result200_response import SubmitResult200Response
from parsedock_client.models.submit_result_request import SubmitResultRequest
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

# Configure API key authorization: internalToken
configuration.api_key['internalToken'] = os.environ["API_KEY"]

# Uncomment below to setup prefix (e.g. Bearer) for API key, if needed
# configuration.api_key_prefix['internalToken'] = 'Bearer'

# Enter a context with an instance of the API client
with parsedock_client.ApiClient(configuration) as api_client:
    # Create an instance of the API class
    api_instance = parsedock_client.InternalApi(api_client)
    id = 'job_06GRQJT5HC1DDZMPWRSKSCKM24' # str | 
    submit_result_request = parsedock_client.SubmitResultRequest() # SubmitResultRequest | 

    try:
        # Submit the Document IR (fenced commit)
        api_response = api_instance.submit_result(id, submit_result_request)
        print("The response of InternalApi->submit_result:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling InternalApi->submit_result: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **id** | **str**|  | 
 **submit_result_request** | [**SubmitResultRequest**](SubmitResultRequest.md)|  | 

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
**200** | Result committed. |  -  |
**409** | Lease expired/superseded — result rejected. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **task_heartbeat**
> TaskHeartbeat200Response task_heartbeat(id, task_heartbeat_request)

Renew the lease and report progress

### Example

* Api Key Authentication (internalToken):

```python
import parsedock_client
from parsedock_client.models.task_heartbeat200_response import TaskHeartbeat200Response
from parsedock_client.models.task_heartbeat_request import TaskHeartbeatRequest
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

# Configure API key authorization: internalToken
configuration.api_key['internalToken'] = os.environ["API_KEY"]

# Uncomment below to setup prefix (e.g. Bearer) for API key, if needed
# configuration.api_key_prefix['internalToken'] = 'Bearer'

# Enter a context with an instance of the API client
with parsedock_client.ApiClient(configuration) as api_client:
    # Create an instance of the API class
    api_instance = parsedock_client.InternalApi(api_client)
    id = 'job_06GRQJT5HC1DDZMPWRSKSCKM24' # str | 
    task_heartbeat_request = parsedock_client.TaskHeartbeatRequest() # TaskHeartbeatRequest | 

    try:
        # Renew the lease and report progress
        api_response = api_instance.task_heartbeat(id, task_heartbeat_request)
        print("The response of InternalApi->task_heartbeat:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling InternalApi->task_heartbeat: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **id** | **str**|  | 
 **task_heartbeat_request** | [**TaskHeartbeatRequest**](TaskHeartbeatRequest.md)|  | 

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
**200** | Lease renewed. |  -  |
**409** | Lease lost or the job was cancelled. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

