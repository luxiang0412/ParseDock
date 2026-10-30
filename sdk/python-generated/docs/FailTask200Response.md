# FailTask200Response


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**job_id** | **str** |  | [optional] 
**status** | **str** |  | [optional] 

## Example

```python
from parsedock_client.models.fail_task200_response import FailTask200Response

# TODO update the JSON string below
json = "{}"
# create an instance of FailTask200Response from a JSON string
fail_task200_response_instance = FailTask200Response.from_json(json)
# print the JSON string representation of the object
print(FailTask200Response.to_json())

# convert the object into a dict
fail_task200_response_dict = fail_task200_response_instance.to_dict()
# create an instance of FailTask200Response from a dict
fail_task200_response_from_dict = FailTask200Response.from_dict(fail_task200_response_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


