# DocumentIRMetrics


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**parse_ms** | **int** |  | [optional] 
**input_bytes** | **int** |  | [optional] 
**output_chars** | **int** |  | [optional] 
**pages** | **int** |  | [optional] 
**peak_rss_mb** | **int** |  | [optional] 
**peak_vram_mb** | **int** |  | [optional] 

## Example

```python
from parsedock_client.models.document_ir_metrics import DocumentIRMetrics

# TODO update the JSON string below
json = "{}"
# create an instance of DocumentIRMetrics from a JSON string
document_ir_metrics_instance = DocumentIRMetrics.from_json(json)
# print the JSON string representation of the object
print(DocumentIRMetrics.to_json())

# convert the object into a dict
document_ir_metrics_dict = document_ir_metrics_instance.to_dict()
# create an instance of DocumentIRMetrics from a dict
document_ir_metrics_from_dict = DocumentIRMetrics.from_dict(document_ir_metrics_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


