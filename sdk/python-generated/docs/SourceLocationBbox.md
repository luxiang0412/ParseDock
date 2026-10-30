# SourceLocationBbox


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**coord** | **str** | Explicit coordinate space; never mix them. | 
**x0** | **float** |  | [optional] 
**y0** | **float** |  | [optional] 
**x1** | **float** |  | [optional] 
**y1** | **float** |  | [optional] 

## Example

```python
from parsedock_client.models.source_location_bbox import SourceLocationBbox

# TODO update the JSON string below
json = "{}"
# create an instance of SourceLocationBbox from a JSON string
source_location_bbox_instance = SourceLocationBbox.from_json(json)
# print the JSON string representation of the object
print(SourceLocationBbox.to_json())

# convert the object into a dict
source_location_bbox_dict = source_location_bbox_instance.to_dict()
# create an instance of SourceLocationBbox from a dict
source_location_bbox_from_dict = SourceLocationBbox.from_dict(source_location_bbox_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


