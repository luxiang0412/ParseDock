# Healthz200Response


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**status** | **str** |  | [optional] 
**role** | **str** |  | [optional] 

## Example

```python
from parsedock_client.models.healthz200_response import Healthz200Response

# TODO update the JSON string below
json = "{}"
# create an instance of Healthz200Response from a JSON string
healthz200_response_instance = Healthz200Response.from_json(json)
# print the JSON string representation of the object
print(Healthz200Response.to_json())

# convert the object into a dict
healthz200_response_dict = healthz200_response_instance.to_dict()
# create an instance of Healthz200Response from a dict
healthz200_response_from_dict = Healthz200Response.from_dict(healthz200_response_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


