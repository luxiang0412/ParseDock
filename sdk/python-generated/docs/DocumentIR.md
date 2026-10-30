# DocumentIR

`doc-ir/0.1`. Missing provenance (page, bbox, sheet, confidence) is absent, never fabricated. See docs/DOCUMENT-IR.md. 

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**schema_version** | **str** |  | 
**parser_engine** | **str** |  | 
**parser_version** | **str** |  | [optional] 
**routing** | [**DocumentIRRouting**](DocumentIRRouting.md) |  | [optional] 
**source** | [**DocumentIRSource**](DocumentIRSource.md) |  | [optional] 
**page_count** | **int** |  | [optional] 
**page_kind** | **str** |  | [optional] 
**elements** | [**List[Element]**](Element.md) |  | 
**tables** | [**List[Table]**](Table.md) |  | [optional] 
**warnings** | [**List[DocumentIRWarningsInner]**](DocumentIRWarningsInner.md) |  | 
**metrics** | [**DocumentIRMetrics**](DocumentIRMetrics.md) |  | 

## Example

```python
from parsedock_client.models.document_ir import DocumentIR

# TODO update the JSON string below
json = "{}"
# create an instance of DocumentIR from a JSON string
document_ir_instance = DocumentIR.from_json(json)
# print the JSON string representation of the object
print(DocumentIR.to_json())

# convert the object into a dict
document_ir_dict = document_ir_instance.to_dict()
# create an instance of DocumentIR from a dict
document_ir_from_dict = DocumentIR.from_dict(document_ir_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


