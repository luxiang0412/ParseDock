# CancelJob202Response


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**job_id** | **str** |  | [optional] 
**status** | **str** |  | [optional] 
**retention_status** | **str** |  | [optional] 
**result_version** | **str** |  | [optional] 
**processing_deadline_at** | **datetime** |  | [optional] 
**message** | **str** |  | [optional] 

## Example

```python
from parsedock_client.models.cancel_job202_response import CancelJob202Response

# TODO update the JSON string below
json = "{}"
# create an instance of CancelJob202Response from a JSON string
cancel_job202_response_instance = CancelJob202Response.from_json(json)
# print the JSON string representation of the object
print(CancelJob202Response.to_json())

# convert the object into a dict
cancel_job202_response_dict = cancel_job202_response_instance.to_dict()
# create an instance of CancelJob202Response from a dict
cancel_job202_response_from_dict = CancelJob202Response.from_dict(cancel_job202_response_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


