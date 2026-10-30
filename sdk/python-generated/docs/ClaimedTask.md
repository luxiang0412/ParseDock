# ClaimedTask


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**job_id** | **str** |  | [optional] 
**attempt_no** | **int** |  | [optional] 
**lease_token** | **str** | Fencing token; required for heartbeat/result/fail | [optional] 
**lease_until** | **datetime** |  | [optional] 
**format** | **str** |  | [optional] 
**mime_type** | **str** |  | [optional] 
**input_name** | **str** |  | [optional] 
**input_sha256** | **str** |  | [optional] 
**input_size** | **int** |  | [optional] 
**page_count** | **int** |  | [optional] 
**requested_mode** | **str** |  | [optional] 
**requested_device** | **str** |  | [optional] 
**engine_hint** | **str** |  | [optional] 
**routing_reason** | **str** |  | [optional] 
**max_lifetime_at** | **datetime** |  | [optional] 

## Example

```python
from parsedock_client.models.claimed_task import ClaimedTask

# TODO update the JSON string below
json = "{}"
# create an instance of ClaimedTask from a JSON string
claimed_task_instance = ClaimedTask.from_json(json)
# print the JSON string representation of the object
print(ClaimedTask.to_json())

# convert the object into a dict
claimed_task_dict = claimed_task_instance.to_dict()
# create an instance of ClaimedTask from a dict
claimed_task_from_dict = ClaimedTask.from_dict(claimed_task_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


