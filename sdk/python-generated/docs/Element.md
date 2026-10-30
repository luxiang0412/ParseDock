# Element


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**id** | **str** |  | 
**type** | **str** |  | 
**text** | **str** |  | 
**level** | **int** | Heading level or list nesting; absent when 0 | [optional] 
**reading_order** | **int** |  | 
**confidence** | **float** | Only when the engine reports it | [optional] 
**source_location** | [**SourceLocation**](SourceLocation.md) |  | [optional] 
**table_id** | **str** |  | [optional] 
**image_ref** | **str** |  | [optional] 
**metadata** | **Dict[str, str]** |  | [optional] 

## Example

```python
from parsedock_client.models.element import Element

# TODO update the JSON string below
json = "{}"
# create an instance of Element from a JSON string
element_instance = Element.from_json(json)
# print the JSON string representation of the object
print(Element.to_json())

# convert the object into a dict
element_dict = element_instance.to_dict()
# create an instance of Element from a dict
element_from_dict = Element.from_dict(element_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


