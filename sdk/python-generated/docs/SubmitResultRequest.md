# SubmitResultRequest


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**lease_token** | **str** |  | 
**engine** | **str** |  | [optional] 
**engine_version** | **str** |  | [optional] 
**device_type** | **str** |  | [optional] 
**page_count** | **int** |  | [optional] 
**document** | [**DocumentIR**](DocumentIR.md) |  | 

## Example

```python
from parsedock_client.models.submit_result_request import SubmitResultRequest

# TODO update the JSON string below
json = "{}"
# create an instance of SubmitResultRequest from a JSON string
submit_result_request_instance = SubmitResultRequest.from_json(json)
# print the JSON string representation of the object
print(SubmitResultRequest.to_json())

# convert the object into a dict
submit_result_request_dict = submit_result_request_instance.to_dict()
# create an instance of SubmitResultRequest from a dict
submit_result_request_from_dict = SubmitResultRequest.from_dict(submit_result_request_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


