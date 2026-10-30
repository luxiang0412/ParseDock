# FailTaskRequest


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**lease_token** | **str** |  | 
**code** | **str** |  | 
**message** | **str** |  | [optional] 
**retryable** | **bool** |  | [optional] 

## Example

```python
from parsedock_client.models.fail_task_request import FailTaskRequest

# TODO update the JSON string below
json = "{}"
# create an instance of FailTaskRequest from a JSON string
fail_task_request_instance = FailTaskRequest.from_json(json)
# print the JSON string representation of the object
print(FailTaskRequest.to_json())

# convert the object into a dict
fail_task_request_dict = fail_task_request_instance.to_dict()
# create an instance of FailTaskRequest from a dict
fail_task_request_from_dict = FailTaskRequest.from_dict(fail_task_request_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


