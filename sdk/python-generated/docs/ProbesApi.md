# parsedock_client.ProbesApi

All URIs are relative to *http://localhost:8080*

Method | HTTP request | Description
------------- | ------------- | -------------
[**healthz**](ProbesApi.md#healthz) | **GET** /healthz | Liveness
[**readyz**](ProbesApi.md#readyz) | **GET** /readyz | Readiness (delegated to the coordinator)


# **healthz**
> Healthz200Response healthz()

Liveness

### Example


```python
import parsedock_client
from parsedock_client.models.healthz200_response import Healthz200Response
from parsedock_client.rest import ApiException
from pprint import pprint

# Defining the host is optional and defaults to http://localhost:8080
# See configuration.py for a list of all supported configuration parameters.
configuration = parsedock_client.Configuration(
    host = "http://localhost:8080"
)


# Enter a context with an instance of the API client
with parsedock_client.ApiClient(configuration) as api_client:
    # Create an instance of the API class
    api_instance = parsedock_client.ProbesApi(api_client)

    try:
        # Liveness
        api_response = api_instance.healthz()
        print("The response of ProbesApi->healthz:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling ProbesApi->healthz: %s\n" % e)
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
**200** | Process is serving. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **readyz**
> Readiness readyz()

Readiness (delegated to the coordinator)

Returns `503` when SQLite is down or the cleaner has stalled. A low disk
watermark keeps the service `ready` (reads and ACKs must keep working) but
reports `accepting_uploads=false`.


### Example


```python
import parsedock_client
from parsedock_client.models.readiness import Readiness
from parsedock_client.rest import ApiException
from pprint import pprint

# Defining the host is optional and defaults to http://localhost:8080
# See configuration.py for a list of all supported configuration parameters.
configuration = parsedock_client.Configuration(
    host = "http://localhost:8080"
)


# Enter a context with an instance of the API client
with parsedock_client.ApiClient(configuration) as api_client:
    # Create an instance of the API class
    api_instance = parsedock_client.ProbesApi(api_client)

    try:
        # Readiness (delegated to the coordinator)
        api_response = api_instance.readyz()
        print("The response of ProbesApi->readyz:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling ProbesApi->readyz: %s\n" % e)
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
**200** | Ready. |  -  |
**503** | Not ready. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

