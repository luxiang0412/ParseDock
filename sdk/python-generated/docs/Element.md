# Element


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**id** | **str** |  | 
**type** | **str** |  | 
**text** | **str** | Plain text with inline markup stripped | 
**rich_text** | **str** | Optional. The same content as &#x60;text&#x60; with CommonMark inline markup (&#x60;**bold**&#x60;, &#x60;*italic*&#x60;, &#x60;&#x60; &#x60;code&#x60; &#x60;&#x60;, &#x60;[text](url)&#x60;, &#x60;~~strike~~&#x60;). Absent when the engine reported no inline formatting; fall back to &#x60;text&#x60;.  | [optional] 
**level** | **int** | Heading level or list nesting; absent when 0 | [optional] 
**reading_order** | **int** |  | 
**confidence** | **float** | Only when the engine reports it | [optional] 
**source_location** | [**SourceLocation**](SourceLocation.md) |  | [optional] 
**table_id** | **str** |  | [optional] 
**image_ref** | **str** |  | [optional] 
**metadata** | **Dict[str, str]** | String hints. Defined keys: &#x60;content_layer&#x3D;furniture&#x60; (page header/footer, skipped by the Markdown and text views), &#x60;markdown&#x3D;blockquote&#x60;, &#x60;list_ordered&#x3D;true&#x60;, &#x60;list_marker&#x60; (e.g. &#x60;3.&#x60;), &#x60;code_language&#x60;, &#x60;recovered&#x3D;text_layer&#x60; (restored from the PDF text layer).  | [optional] 

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


