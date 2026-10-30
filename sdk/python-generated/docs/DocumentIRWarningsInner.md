# DocumentIRWarningsInner


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**code** | **str** |  | [optional] 
**message** | **str** |  | [optional] 
**severity** | **str** |  | [optional] 
**source_location** | [**SourceLocation**](SourceLocation.md) |  | [optional] 

## Example

```python
from parsedock_client.models.document_ir_warnings_inner import DocumentIRWarningsInner

# TODO update the JSON string below
json = "{}"
# create an instance of DocumentIRWarningsInner from a JSON string
document_ir_warnings_inner_instance = DocumentIRWarningsInner.from_json(json)
# print the JSON string representation of the object
print(DocumentIRWarningsInner.to_json())

# convert the object into a dict
document_ir_warnings_inner_dict = document_ir_warnings_inner_instance.to_dict()
# create an instance of DocumentIRWarningsInner from a dict
document_ir_warnings_inner_from_dict = DocumentIRWarningsInner.from_dict(document_ir_warnings_inner_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


