# Table


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**id** | **str** |  | [optional] 
**rows** | **int** |  | [optional] 
**cols** | **int** |  | [optional] 
**header_rows** | **int** |  | [optional] 
**caption** | **str** |  | [optional] 
**confidence** | **float** |  | [optional] 
**source_location** | [**SourceLocation**](SourceLocation.md) |  | [optional] 
**cells** | [**List[TableCellsInner]**](TableCellsInner.md) |  | [optional] 

## Example

```python
from parsedock_client.models.table import Table

# TODO update the JSON string below
json = "{}"
# create an instance of Table from a JSON string
table_instance = Table.from_json(json)
# print the JSON string representation of the object
print(Table.to_json())

# convert the object into a dict
table_dict = table_instance.to_dict()
# create an instance of Table from a dict
table_from_dict = Table.from_dict(table_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


