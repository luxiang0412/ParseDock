# parsedock_client.SyncApi

All URIs are relative to *http://localhost:8080*

Method | HTTP request | Description
------------- | ------------- | -------------
[**parse_sync**](SyncApi.md#parse_sync) | **POST** /v1/parse | Bounded synchronous parse (small documents)


# **parse_sync**
> DocumentIR parse_sync(file, mode=mode, device=device, ttl_seconds=ttl_seconds, priority=priority, idempotency_key=idempotency_key, timeout_ms=timeout_ms)

Bounded synchronous parse (small documents)

Same validation and pipeline as `POST /v1/jobs`, but waits up to
`timeout_ms` (default 30 s, max 2 min) for completion.
* finished → `200` with the Document IR;
* deadline reached → `202` with the job resource; it keeps running asynchronously;
* failure → the job's stable error code, with the same status the code has
  everywhere else: `422` for document and engine failures (`malformed_file`,
  `encrypted_file`, `page_limit_exceeded`, `engine_*`, `timeout`,
  `internal_error`), `413`/`415` for size and type, `429 quota_exceeded` with
  `Retry-After` when the result does not fit the storage quota, and
  `409 cancelled` when the job was cancelled while the call waited.

The result is **not released** by this call: a written response does not
prove the caller received it, so the normal TTL applies. The response
carries `X-Job-Id` and `X-Result-Version`; release with
`POST /v1/jobs/{id}/ack` when your sink has the bytes. Intended for
interactive callers; prefer `/v1/jobs` for anything large.


### Example

* Api Key Authentication (apiKeyAuth):
* Bearer Authentication (bearerAuth):

```python
import parsedock_client
from parsedock_client.models.document_ir import DocumentIR
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

# Configure API key authorization: apiKeyAuth
configuration.api_key['apiKeyAuth'] = os.environ["API_KEY"]

# Uncomment below to setup prefix (e.g. Bearer) for API key, if needed
# configuration.api_key_prefix['apiKeyAuth'] = 'Bearer'

# Configure Bearer authorization: bearerAuth
configuration = parsedock_client.Configuration(
    access_token = os.environ["BEARER_TOKEN"]
)

# Enter a context with an instance of the API client
with parsedock_client.ApiClient(configuration) as api_client:
    # Create an instance of the API class
    api_instance = parsedock_client.SyncApi(api_client)
    file = None # bytes | 
    mode = 'auto' # str | A **policy preference, not a per-document accuracy guarantee**:  * `fast` — fastest engine that can read the format (xberg for PDF/Office). * `accurate` — requires an engine that declares structured output   (docling-rs: reading order, bbox, table spans, sheet names). * `auto` — router picks by priority and records why.  Text formats (`txt`, `md`, `csv`, `tsv`) always use the native engine in every mode: it is orders of magnitude faster and decodes GB18030/UTF-16 correctly, which the ML engines do not. Measured counter-examples where `accurate` is *not* more accurate (OCR-heavy scans, DOCX/XLSX text) are documented in `docs/ENGINE-BENCHMARK.md`; the engine, version and reason are always recorded in the job and the IR.  (optional) (default to 'auto')
    device = 'auto' # str | `gpu_required` is rejected with `409 gpu_unavailable` when no GPU worker is registered — it never silently runs on CPU. `gpu_preferred` may fall back to CPU, and the result marks the actual device.  (optional) (default to 'auto')
    ttl_seconds = 56 # int | Requested result TTL, capped by the server's maximum. A non-integer or a value below 1 is rejected with `400 invalid_request`.  (optional)
    priority = 0 # int | Same as on `POST /v1/jobs`. A non-integer or a value outside 0–100 is rejected with `400 invalid_request`.  (optional) (default to 0)
    idempotency_key = 'idempotency_key_example' # str | Same key + same owner + same request returns the original job (`200`) instead of creating a second one. \"Same request\" means the same input bytes, `mode` and `device`; reusing the key for anything else is `422 idempotency_key_mismatch` (`ttl_seconds` and `priority` are not compared). Longer than 200 characters is rejected with `400 invalid_request`; keys are never truncated, so distinct long keys cannot collide.  (optional)
    timeout_ms = 30000 # int |  (optional) (default to 30000)

    try:
        # Bounded synchronous parse (small documents)
        api_response = api_instance.parse_sync(file, mode=mode, device=device, ttl_seconds=ttl_seconds, priority=priority, idempotency_key=idempotency_key, timeout_ms=timeout_ms)
        print("The response of SyncApi->parse_sync:\n")
        pprint(api_response)
    except Exception as e:
        print("Exception when calling SyncApi->parse_sync: %s\n" % e)
```



### Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **file** | **bytes**|  | 
 **mode** | **str**| A **policy preference, not a per-document accuracy guarantee**:  * &#x60;fast&#x60; — fastest engine that can read the format (xberg for PDF/Office). * &#x60;accurate&#x60; — requires an engine that declares structured output   (docling-rs: reading order, bbox, table spans, sheet names). * &#x60;auto&#x60; — router picks by priority and records why.  Text formats (&#x60;txt&#x60;, &#x60;md&#x60;, &#x60;csv&#x60;, &#x60;tsv&#x60;) always use the native engine in every mode: it is orders of magnitude faster and decodes GB18030/UTF-16 correctly, which the ML engines do not. Measured counter-examples where &#x60;accurate&#x60; is *not* more accurate (OCR-heavy scans, DOCX/XLSX text) are documented in &#x60;docs/ENGINE-BENCHMARK.md&#x60;; the engine, version and reason are always recorded in the job and the IR.  | [optional] [default to &#39;auto&#39;]
 **device** | **str**| &#x60;gpu_required&#x60; is rejected with &#x60;409 gpu_unavailable&#x60; when no GPU worker is registered — it never silently runs on CPU. &#x60;gpu_preferred&#x60; may fall back to CPU, and the result marks the actual device.  | [optional] [default to &#39;auto&#39;]
 **ttl_seconds** | **int**| Requested result TTL, capped by the server&#39;s maximum. A non-integer or a value below 1 is rejected with &#x60;400 invalid_request&#x60;.  | [optional] 
 **priority** | **int**| Same as on &#x60;POST /v1/jobs&#x60;. A non-integer or a value outside 0–100 is rejected with &#x60;400 invalid_request&#x60;.  | [optional] [default to 0]
 **idempotency_key** | **str**| Same key + same owner + same request returns the original job (&#x60;200&#x60;) instead of creating a second one. \&quot;Same request\&quot; means the same input bytes, &#x60;mode&#x60; and &#x60;device&#x60;; reusing the key for anything else is &#x60;422 idempotency_key_mismatch&#x60; (&#x60;ttl_seconds&#x60; and &#x60;priority&#x60; are not compared). Longer than 200 characters is rejected with &#x60;400 invalid_request&#x60;; keys are never truncated, so distinct long keys cannot collide.  | [optional] 
 **timeout_ms** | **int**|  | [optional] [default to 30000]

### Return type

[**DocumentIR**](DocumentIR.md)

### Authorization

[apiKeyAuth](../README.md#apiKeyAuth), [bearerAuth](../README.md#bearerAuth)

### HTTP request headers

 - **Content-Type**: multipart/form-data
 - **Accept**: application/json

### HTTP response details

| Status code | Description | Response headers |
|-------------|-------------|------------------|
**200** | Parsed document (IR). |  -  |
**202** | Still running; the job continues in the background. |  -  |
**400** | Invalid parameters. |  -  |
**401** | Missing/invalid token. |  -  |
**403** | The API key is valid but does not grant the scope this operation needs. |  -  |
**409** | &#x60;cancelled&#x60; — the job was cancelled before it finished; &#x60;conflict&#x60; — the idempotency key was already used for this same request (use &#x60;/v1/jobs&#x60; for async access to that job); &#x60;gpu_unavailable&#x60; — &#x60;device&#x3D;gpu_required&#x60; with no GPU worker.  |  -  |
**413** | Upload exceeds the configured limit. |  -  |
**415** | Format not in the allow-list, or content does not match the extension. |  -  |
**422** | Parse failed, or the document is malformed, encrypted or over a limit, or &#x60;idempotency_key_mismatch&#x60; (the key was used for a different request).  |  -  |
**429** | Per-owner limits. &#x60;rate_limited&#x60; &#x3D; request-rate budget (all requests, including status polling and event streams); &#x60;quota_exceeded&#x60; &#x3D; concurrent jobs, jobs/day or bytes/day for this owner. Both carry &#x60;Retry-After&#x60; in seconds. Retrying &#x60;rate_limited&#x60; after that delay is expected; retrying &#x60;quota_exceeded&#x60; is not - the quota only resets when its window passes.  |  * Retry-After -  <br>  |
**507** | Storage below the safety watermark; retry later. |  -  |

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to Model list]](../README.md#documentation-for-models) [[Back to README]](../README.md)

