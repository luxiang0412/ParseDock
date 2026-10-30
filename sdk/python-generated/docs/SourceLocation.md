# SourceLocation


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**kind** | **str** |  | [optional] 
**page** | **int** |  | [optional] 
**bbox** | [**SourceLocationBbox**](SourceLocationBbox.md) |  | [optional] 
**sheet** | **str** |  | [optional] 
**cell_range** | **str** |  | [optional] 
**line** | **int** |  | [optional] 

## Example

```python
from parsedock_client.models.source_location import SourceLocation

# TODO update the JSON string below
json = "{}"
# create an instance of SourceLocation from a JSON string
source_location_instance = SourceLocation.from_json(json)
# print the JSON string representation of the object
print(SourceLocation.to_json())

# convert the object into a dict
source_location_dict = source_location_instance.to_dict()
# create an instance of SourceLocation from a dict
source_location_from_dict = SourceLocation.from_dict(source_location_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


