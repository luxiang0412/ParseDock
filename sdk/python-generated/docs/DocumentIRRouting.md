# DocumentIRRouting


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**requested_mode** | **str** |  | [optional] 
**requested_device** | **str** |  | [optional] 
**actual_device** | **str** |  | [optional] 
**engine** | **str** |  | [optional] 
**fallback** | **bool** |  | [optional] 
**reason** | **str** |  | [optional] 

## Example

```python
from parsedock_client.models.document_ir_routing import DocumentIRRouting

# TODO update the JSON string below
json = "{}"
# create an instance of DocumentIRRouting from a JSON string
document_ir_routing_instance = DocumentIRRouting.from_json(json)
# print the JSON string representation of the object
print(DocumentIRRouting.to_json())

# convert the object into a dict
document_ir_routing_dict = document_ir_routing_instance.to_dict()
# create an instance of DocumentIRRouting from a dict
document_ir_routing_from_dict = DocumentIRRouting.from_dict(document_ir_routing_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


