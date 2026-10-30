# parsedock_client.JobsApi

All URIs are relative to *http://localhost:8080*

Method | HTTP request | Description
------------- | ------------- | -------------
[**ack_job**](JobsApi.md#ack_job) | **POST** /v1/jobs/{id}/ack | Accept that the client consumed the result, release storage asynchronously
[**cancel_job**](JobsApi.md#cancel_job) | **POST** /v1/jobs/{id}/cancel | Stop a queued/running job (completed results must be ACKed)
[**create_job**](JobsApi.md#create_job) | **POST** /v1/jobs | Upload a document and enqueue a parse job
[**download_result**](JobsApi.md#download_result) | **GET** /v1/jobs/{id}/result | Download a derived view of the committed result
[**get_job**](JobsApi.md#get_job) | **GET** /v1/jobs/{id} | Job status, retention state and result availability


# **ack_job**
> AckJob202Response ack_job(id, ack_job_request, idempotency_key=idempotency_key)

Accept that the client consumed the result, release storage asynchronously

Idempotent. The first ACK must carry the `result_version` that was
downloaded (mismatch → `409`, nothing is released). The response means
"release intent accepted"; poll `GET /v1/jobs/{id}` until
`retention_status` is `released`.

After a successful ACK, new downloads return `410` while in-flight
downloads keep their lease. Repeat ACKs return `202` again and affect
nothing else.


### Example

* Bearer Authentication (bearerAuth):

```python
import parsedock_client
from parsedock_client.models.ack_job202_response import AckJob202Response
from parsedock_client.models.ack_job_request import AckJobRequest
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
    api_instance = parsedock_client.JobsApi(api_client)
    id = 'job_06GRQJT5HC1DDZMPWRSKSCKM24' # str | 
    ack_job_request = parsedock_client.AckJobRequest() # AckJobRequest | 
    idempotency_key = 'idempotency_key_example' # str | Same key + same owner returns the original job (`200`) instead of creating a second one. (optional)

    try:
        # Accept that the client consumed the result, release storage asynchronously
        api_response = api_instance.ack_job(id, ack_job_request, idempotency_key=idempotency_key)
        print("The response of JobsApi->ack_job:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling JobsApi->ack_job: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **id** | **str**|  | 
 **ack_job_request** | [**AckJobRequest**](AckJobRequest.md)|  | 
 **idempotency_key** | **str**| Same key + same owner returns the original job (&#x60;200&#x60;) instead of creating a second one. | [optional] 

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
**202** | Release accepted (not necessarily deleted yet). |  -  |
**400** | Invalid parameters. |  -  |
**401** | Missing/invalid token. |  -  |
**403** | The API key is valid but does not grant the scope this operation needs. |  -  |
**404** | Unknown job, or a job owned by somebody else. |  -  |
**409** | result_version does not match the current result. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **cancel_job**
> CancelJob202Response cancel_job(id, idempotency_key=idempotency_key, cancel_job_request=cancel_job_request)

Stop a queued/running job (completed results must be ACKed)

Best effort and idempotent for `queued`/`running` jobs: the job becomes
`cancelled` immediately and `cancel_requested_at` is recorded; the worker
sees `cancelled` on its next heartbeat (default 10 s), aborts the engine
process and finalises cleanly. Files are removed by the normal retention
path. A cancelled job never publishes a result, even if the engine finished
in the meantime.

A **completed** result is not cancelled. It is released, which is the same
operation as ACK and therefore requires `result_version`; without it the
call returns `409` and nothing is released. Use `POST /ack`.


### Example

* Bearer Authentication (bearerAuth):

```python
import parsedock_client
from parsedock_client.models.cancel_job202_response import CancelJob202Response
from parsedock_client.models.cancel_job_request import CancelJobRequest
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
    api_instance = parsedock_client.JobsApi(api_client)
    id = 'job_06GRQJT5HC1DDZMPWRSKSCKM24' # str | 
    idempotency_key = 'idempotency_key_example' # str | Same key + same owner returns the original job (`200`) instead of creating a second one. (optional)
    cancel_job_request = parsedock_client.CancelJobRequest() # CancelJobRequest |  (optional)

    try:
        # Stop a queued/running job (completed results must be ACKed)
        api_response = api_instance.cancel_job(id, idempotency_key=idempotency_key, cancel_job_request=cancel_job_request)
        print("The response of JobsApi->cancel_job:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling JobsApi->cancel_job: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **id** | **str**|  | 
 **idempotency_key** | **str**| Same key + same owner returns the original job (&#x60;200&#x60;) instead of creating a second one. | [optional] 
 **cancel_job_request** | [**CancelJobRequest**](CancelJobRequest.md)|  | [optional] 

### Return type

[**CancelJob202Response**](CancelJob202Response.md)

### Authorization

[bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: application/json
 - **Accept**: application/json

### HTTP response details

| Status code | Description | Response headers |
|-------------|-------------|------------------|
**202** | Cancellation (or release) accepted. |  -  |
**401** | Missing/invalid token. |  -  |
**403** | The API key is valid but does not grant the scope this operation needs. |  -  |
**404** | Unknown job, or a job owned by somebody else. |  -  |
**409** | Job already completed — cancel needs &#x60;result_version&#x60;. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **create_job**
> Job create_job(file, mode=mode, device=device, ttl_seconds=ttl_seconds, priority=priority, idempotency_key=idempotency_key, mode2=mode2, device2=device2, ttl_seconds2=ttl_seconds2, idempotency_key2=idempotency_key2)

Upload a document and enqueue a parse job

`multipart/form-data`. Parts may arrive in **any order**: the file is
streamed to the coordinator's staging area (never buffered in memory)
while scalar fields are collected, and the job is created once the body
has been read.

Precedence for `mode`/`device`/`priority`/`ttl_seconds`/`idempotency_key`:
query parameter → multipart field → default. The `Idempotency-Key`
header wins over both.

Upload limits are enforced server-side: size (`max_upload_mb`), page count
(`max_pages`), archive expansion ratio, OOXML structure and PDF markers.
The declared `Content-Type` of the file part is **not** trusted.


### Example

* Bearer Authentication (bearerAuth):

```python
import parsedock_client
from parsedock_client.models.job import Job
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
    api_instance = parsedock_client.JobsApi(api_client)
    file = None # bytes | The document. Required.
    mode = 'auto' # str | A **policy preference, not a per-document accuracy guarantee**:  * `fast` — fastest engine that can read the format (xberg for PDF/Office). * `accurate` — requires an engine that declares structured output   (docling-rs: reading order, bbox, table spans, sheet names). * `auto` — router picks by priority and records why.  Text formats (`txt`, `md`, `csv`, `tsv`) always use the native engine in every mode: it is orders of magnitude faster and decodes GB18030/UTF-16 correctly, which the ML engines do not. Measured counter-examples where `accurate` is *not* more accurate (OCR-heavy scans, DOCX/XLSX text) are documented in `docs/ENGINE-BENCHMARK.md`; the engine, version and reason are always recorded in the job and the IR.  (optional) (default to 'auto')
    device = 'auto' # str | `gpu_required` is rejected with `409 gpu_unavailable` when no GPU worker is registered — it never silently runs on CPU. `gpu_preferred` may fall back to CPU, and the result marks the actual device.  (optional) (default to 'auto')
    ttl_seconds = 56 # int | Requested result TTL, capped by the server's maximum. (optional)
    priority = 0 # int | Higher runs first within the same queue. (optional) (default to 0)
    idempotency_key = 'idempotency_key_example' # str | Same key + same owner returns the original job (`200`) instead of creating a second one. (optional)
    mode2 = 'auto' # str |  (optional) (default to 'auto')
    device2 = 'auto' # str |  (optional) (default to 'auto')
    ttl_seconds2 = 56 # int | Requested result TTL; capped by the server maximum. (optional)
    idempotency_key2 = 'idempotency_key_example' # str | Alternative to the Idempotency-Key header. (optional)

    try:
        # Upload a document and enqueue a parse job
        api_response = api_instance.create_job(file, mode=mode, device=device, ttl_seconds=ttl_seconds, priority=priority, idempotency_key=idempotency_key, mode2=mode2, device2=device2, ttl_seconds2=ttl_seconds2, idempotency_key2=idempotency_key2)
        print("The response of JobsApi->create_job:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling JobsApi->create_job: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **file** | **bytes**| The document. Required. | 
 **mode** | **str**| A **policy preference, not a per-document accuracy guarantee**:  * &#x60;fast&#x60; — fastest engine that can read the format (xberg for PDF/Office). * &#x60;accurate&#x60; — requires an engine that declares structured output   (docling-rs: reading order, bbox, table spans, sheet names). * &#x60;auto&#x60; — router picks by priority and records why.  Text formats (&#x60;txt&#x60;, &#x60;md&#x60;, &#x60;csv&#x60;, &#x60;tsv&#x60;) always use the native engine in every mode: it is orders of magnitude faster and decodes GB18030/UTF-16 correctly, which the ML engines do not. Measured counter-examples where &#x60;accurate&#x60; is *not* more accurate (OCR-heavy scans, DOCX/XLSX text) are documented in &#x60;docs/ENGINE-BENCHMARK.md&#x60;; the engine, version and reason are always recorded in the job and the IR.  | [optional] [default to &#39;auto&#39;]
 **device** | **str**| &#x60;gpu_required&#x60; is rejected with &#x60;409 gpu_unavailable&#x60; when no GPU worker is registered — it never silently runs on CPU. &#x60;gpu_preferred&#x60; may fall back to CPU, and the result marks the actual device.  | [optional] [default to &#39;auto&#39;]
 **ttl_seconds** | **int**| Requested result TTL, capped by the server&#39;s maximum. | [optional] 
 **priority** | **int**| Higher runs first within the same queue. | [optional] [default to 0]
 **idempotency_key** | **str**| Same key + same owner returns the original job (&#x60;200&#x60;) instead of creating a second one. | [optional] 
 **mode2** | **str**|  | [optional] [default to &#39;auto&#39;]
 **device2** | **str**|  | [optional] [default to &#39;auto&#39;]
 **ttl_seconds2** | **int**| Requested result TTL; capped by the server maximum. | [optional] 
 **idempotency_key2** | **str**| Alternative to the Idempotency-Key header. | [optional] 

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
**202** | Job accepted and queued. |  * Location - Relative URL of the job resource. <br>  |
**200** | Idempotent replay — the existing job is returned, nothing new was created. |  -  |
**400** | Invalid parameters. |  -  |
**401** | Missing/invalid token. |  -  |
**403** | The API key is valid but does not grant the scope this operation needs. |  -  |
**409** | A GPU worker is required but none is registered, or the idempotency key conflicts. |  -  |
**413** | Upload exceeds the configured limit. |  -  |
**415** | Format not in the allow-list, or content does not match the extension. |  -  |
**422** | Malformed, encrypted or structurally invalid document. |  -  |
**429** | Per-owner limits. &#x60;rate_limited&#x60; &#x3D; request-rate budget (all requests, including status polling); &#x60;quota_exceeded&#x60; &#x3D; concurrent jobs, jobs/day or bytes/day for this owner. Both carry &#x60;Retry-After&#x60; in seconds.  |  * Retry-After -  <br>  |
**507** | Storage below the safety watermark; retry later (existing results stay readable). |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **download_result**
> str download_result(id, format=format)

Download a derived view of the committed result

Streams the artifact and holds a **download lease** until the body is
complete (renewed while streaming). An ACK during the stream does not
truncate it; the cleaner waits for the lease to end before deleting.

Response headers:
* `ETag` — strong validator **of this representation** (`sha256` of the
  markdown/text/json bytes). Different formats have different ETags.
* `X-Result-Version` — the manifest hash, identical for all three formats
  and the only value `POST /ack` accepts.
* `Content-Length`, `X-Download-Lease`, `Vary: Accept-Encoding`.

Conditional requests: send `If-None-Match` with a previously received
`ETag` to get `304 Not Modified` (no body). Repeated downloads are
allowed while `retention_status=active`.


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
    api_instance = parsedock_client.JobsApi(api_client)
    id = 'job_06GRQJT5HC1DDZMPWRSKSCKM24' # str | 
    format = 'markdown' # str | `json` is the canonical Document IR (`doc-ir/0.1`); `markdown`/`text` are rendered from the IR.  (optional) (default to 'markdown')

    try:
        # Download a derived view of the committed result
        api_response = api_instance.download_result(id, format=format)
        print("The response of JobsApi->download_result:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling JobsApi->download_result: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **id** | **str**|  | 
 **format** | **str**| &#x60;json&#x60; is the canonical Document IR (&#x60;doc-ir/0.1&#x60;); &#x60;markdown&#x60;/&#x60;text&#x60; are rendered from the IR.  | [optional] [default to &#39;markdown&#39;]

### Return type

**str**

### Authorization

[bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: Not defined
 - **Accept**: text/markdown, text/plain, application/json

### HTTP response details

| Status code | Description | Response headers |
|-------------|-------------|------------------|
**304** | The client&#39;s &#x60;If-None-Match&#x60; matches the current artifact. |  * ETag - Strong validator of this representation (per-artifact sha256). <br>  * X-Result-Version - Manifest hash, identical for all formats; echo this in the ACK. <br>  |
**200** | The requested representation. |  * ETag - Strong validator of this representation (per-artifact sha256). <br>  * X-Result-Version - Manifest hash, identical for all formats; echo this in the ACK. <br>  * X-Download-Lease -  <br>  * Content-Length -  <br>  |
**401** | Missing/invalid token. |  -  |
**403** | The API key is valid but does not grant the scope this operation needs. |  -  |
**404** | Unknown job, or a job owned by somebody else. |  -  |
**409** | The result is not ready yet (job queued/running/failed). |  -  |
**410** | The result was released or expired (tombstone window). |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

# **get_job**
> Job get_job(id)

Job status, retention state and result availability

The authoritative view of a job. Use this instead of trusting the last
SSE event: events are best-effort, the query is not.


### Example

* Bearer Authentication (bearerAuth):

```python
import parsedock_client
from parsedock_client.models.job import Job
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
    api_instance = parsedock_client.JobsApi(api_client)
    id = 'job_06GRQJT5HC1DDZMPWRSKSCKM24' # str | 

    try:
        # Job status, retention state and result availability
        api_response = api_instance.get_job(id)
        print("The response of JobsApi->get_job:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling JobsApi->get_job: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **id** | **str**|  | 

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
**200** | Current job state. |  -  |
**401** | Missing/invalid token. |  -  |
**403** | The API key is valid but does not grant the scope this operation needs. |  -  |
**404** | Unknown job, or a job owned by somebody else. |  -  |
**503** | The coordinator is unreachable (API replicas stay healthy). |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

