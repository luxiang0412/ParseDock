# Job


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**job_id** | **str** |  | 
**status** | **str** | Processing dimension. | 
**processing_status** | **str** | Alias of &#x60;status&#x60; (both are returned). | [optional] 
**retention_status** | **str** | Retention dimension, independent of &#x60;status&#x60;. | 
**input_name** | **str** |  | [optional] 
**format** | **str** |  | [optional] 
**mime_type** | **str** |  | [optional] 
**input_size** | **int** |  | [optional] 
**input_sha256** | **str** |  | [optional] 
**page_count** | **int** | Engine-reported when available | [optional] 
**created_at** | **datetime** |  | 
**started_at** | **datetime** |  | [optional] 
**completed_at** | **datetime** |  | [optional] 
**updated_at** | **datetime** |  | [optional] 
**result_expires_at** | **datetime** | TTL measured from result commit, not upload. | [optional] 
**processing_deadline_at** | **datetime** | Deadline for **queued/running** work only (created + max_job_lifetime). It does not bound a completed result: retention is governed by &#x60;result_expires_at&#x60;.  | [optional] 
**max_lifetime_at** | **datetime** | Deprecated alias of &#x60;processing_deadline_at&#x60; (same value). | [optional] 
**attempt** | **int** |  | [optional] 
**requested_mode** | **str** |  | [optional] 
**requested_device** | **str** |  | [optional] 
**engine** | **str** | Engine that actually produced the result | [optional] 
**routing_reason** | **str** | Human-readable routing decision | [optional] 
**progress_percent** | **int** |  | [optional] 
**progress_stage** | **str** |  | [optional] 
**cancel_requested_at** | **datetime** | Set when cancellation was requested (queued/running jobs). The worker stops at its next heartbeat (default interval 10 s) and finalises the attempt; the job is already &#x60;cancelled&#x60; when this field appears.  | [optional] 
**result_version** | **str** | Manifest hash; echo it in the ACK (&#x60;X-Result-Version&#x60;). | [optional] 
**result_urls** | [**JobResultUrls**](JobResultUrls.md) |  | [optional] 
**warning_count** | **int** |  | [optional] 
**error_code** | **str** |  | [optional] 
**error_message** | **str** |  | [optional] 
**status_url** | **str** |  | 
**events_url** | **str** |  | [optional] 
**ack_url** | **str** |  | [optional] 
**cancel_url** | **str** |  | [optional] 

## Example

```python
from parsedock_client.models.job import Job

# TODO update the JSON string below
json = "{}"
# create an instance of Job from a JSON string
job_instance = Job.from_json(json)
# print the JSON string representation of the object
print(Job.to_json())

# convert the object into a dict
job_dict = job_instance.to_dict()
# create an instance of Job from a dict
job_from_dict = Job.from_dict(job_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


