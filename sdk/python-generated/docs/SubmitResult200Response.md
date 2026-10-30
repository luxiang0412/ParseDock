# SubmitResult200Response


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**job_id** | **str** |  | [optional] 
**status** | **str** |  | [optional] 
**result_version** | **str** |  | [optional] 
**result_expires_at** | **datetime** |  | [optional] 

## Example

```python
from parsedock_client.models.submit_result200_response import SubmitResult200Response

# TODO update the JSON string below
json = "{}"
# create an instance of SubmitResult200Response from a JSON string
submit_result200_response_instance = SubmitResult200Response.from_json(json)
# print the JSON string representation of the object
print(SubmitResult200Response.to_json())

# convert the object into a dict
submit_result200_response_dict = submit_result200_response_instance.to_dict()
# create an instance of SubmitResult200Response from a dict
submit_result200_response_from_dict = SubmitResult200Response.from_dict(submit_result200_response_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


