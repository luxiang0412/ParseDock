# TableCellsInner


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**row** | **int** |  | [optional] 
**col** | **int** |  | [optional] 
**row_span** | **int** |  | [optional] 
**col_span** | **int** |  | [optional] 
**text** | **str** |  | [optional] 
**rich_text** | **str** | Optional cell text with CommonMark inline markup | [optional] 
**formula** | **str** |  | [optional] 
**cached_value** | **str** |  | [optional] 
**data_type** | **str** |  | [optional] 
**confidence** | **float** |  | [optional] 

## Example

```python
from parsedock_client.models.table_cells_inner import TableCellsInner

# TODO update the JSON string below
json = "{}"
# create an instance of TableCellsInner from a JSON string
table_cells_inner_instance = TableCellsInner.from_json(json)
# print the JSON string representation of the object
print(TableCellsInner.to_json())

# convert the object into a dict
table_cells_inner_dict = table_cells_inner_instance.to_dict()
# create an instance of TableCellsInner from a dict
table_cells_inner_from_dict = TableCellsInner.from_dict(table_cells_inner_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


