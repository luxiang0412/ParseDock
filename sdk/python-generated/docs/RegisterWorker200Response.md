# RegisterWorker200Response


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**worker_id** | **str** |  | [optional] 
**session_expires** | **datetime** |  | [optional] 
**heartbeat_every** | **str** |  | [optional] 

## Example

```python
from parsedock_client.models.register_worker200_response import RegisterWorker200Response

# TODO update the JSON string below
json = "{}"
# create an instance of RegisterWorker200Response from a JSON string
register_worker200_response_instance = RegisterWorker200Response.from_json(json)
# print the JSON string representation of the object
print(RegisterWorker200Response.to_json())

# convert the object into a dict
register_worker200_response_dict = register_worker200_response_instance.to_dict()
# create an instance of RegisterWorker200Response from a dict
register_worker200_response_from_dict = RegisterWorker200Response.from_dict(register_worker200_response_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


