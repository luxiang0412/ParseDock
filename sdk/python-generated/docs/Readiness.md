# Readiness


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**status** | **str** |  | [optional] 
**checks** | **Dict[str, object]** |  | [optional] 

## Example

```python
from parsedock_client.models.readiness import Readiness

# TODO update the JSON string below
json = "{}"
# create an instance of Readiness from a JSON string
readiness_instance = Readiness.from_json(json)
# print the JSON string representation of the object
print(Readiness.to_json())

# convert the object into a dict
readiness_dict = readiness_instance.to_dict()
# create an instance of Readiness from a dict
readiness_from_dict = Readiness.from_dict(readiness_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


