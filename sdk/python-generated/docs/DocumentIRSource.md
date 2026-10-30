# DocumentIRSource


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**file_name** | **str** |  | [optional] 
**mime_type** | **str** |  | [optional] 
**format** | **str** |  | [optional] 
**size_bytes** | **int** |  | [optional] 
**sha256** | **str** |  | [optional] 

## Example

```python
from parsedock_client.models.document_ir_source import DocumentIRSource

# TODO update the JSON string below
json = "{}"
# create an instance of DocumentIRSource from a JSON string
document_ir_source_instance = DocumentIRSource.from_json(json)
# print the JSON string representation of the object
print(DocumentIRSource.to_json())

# convert the object into a dict
document_ir_source_dict = document_ir_source_instance.to_dict()
# create an instance of DocumentIRSource from a dict
document_ir_source_from_dict = DocumentIRSource.from_dict(document_ir_source_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


