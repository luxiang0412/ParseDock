

# Element


## Properties

| Name | Type | Description | Notes |
|------------ | ------------- | ------------- | -------------|
|**id** | **String** |  |  |
|**type** | [**TypeEnum**](#TypeEnum) |  |  |
|**text** | **String** |  |  |
|**level** | **Integer** | Heading level or list nesting; absent when 0 |  [optional] |
|**readingOrder** | **Integer** |  |  |
|**confidence** | **BigDecimal** | Only when the engine reports it |  [optional] |
|**sourceLocation** | [**SourceLocation**](SourceLocation.md) |  |  [optional] |
|**tableId** | **String** |  |  [optional] |
|**imageRef** | **String** |  |  [optional] |
|**metadata** | **Map&lt;String, String&gt;** |  |  [optional] |



## Enum: TypeEnum

| Name | Value |
|---- | -----|
| TITLE | &quot;title&quot; |
| SECTION | &quot;section&quot; |
| PARAGRAPH | &quot;paragraph&quot; |
| LIST | &quot;list&quot; |
| LIST_ITEM | &quot;list_item&quot; |
| TABLE | &quot;table&quot; |
| IMAGE | &quot;image&quot; |
| FORMULA | &quot;formula&quot; |
| CAPTION | &quot;caption&quot; |
| FOOTNOTE | &quot;footnote&quot; |
| HEADER | &quot;header&quot; |
| FOOTER | &quot;footer&quot; |
| CODE | &quot;code&quot; |
| PAGE_BREAK | &quot;page_break&quot; |
| UNKNOWN | &quot;unknown&quot; |



