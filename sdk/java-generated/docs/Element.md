

# Element


## Properties

| Name | Type | Description | Notes |
|------------ | ------------- | ------------- | -------------|
|**id** | **String** |  |  |
|**type** | [**TypeEnum**](#TypeEnum) |  |  |
|**text** | **String** | Plain text with inline markup stripped |  |
|**richText** | **String** | Optional. The same content as &#x60;text&#x60; with CommonMark inline markup (&#x60;**bold**&#x60;, &#x60;*italic*&#x60;, &#x60;&#x60; &#x60;code&#x60; &#x60;&#x60;, &#x60;[text](url)&#x60;, &#x60;~~strike~~&#x60;). Absent when the engine reported no inline formatting; fall back to &#x60;text&#x60;.  |  [optional] |
|**level** | **Integer** | Heading level or list nesting; absent when 0 |  [optional] |
|**readingOrder** | **Integer** |  |  |
|**confidence** | **BigDecimal** | Only when the engine reports it |  [optional] |
|**sourceLocation** | [**SourceLocation**](SourceLocation.md) |  |  [optional] |
|**tableId** | **String** |  |  [optional] |
|**imageRef** | **String** |  |  [optional] |
|**metadata** | **Map&lt;String, String&gt;** | String hints. Defined keys: &#x60;content_layer&#x3D;furniture&#x60; (page header/footer, skipped by the Markdown and text views), &#x60;markdown&#x3D;blockquote&#x60;, &#x60;list_ordered&#x3D;true&#x60;, &#x60;list_marker&#x60; (e.g. &#x60;3.&#x60;), &#x60;code_language&#x60;, &#x60;recovered&#x3D;text_layer&#x60; (restored from the PDF text layer).  |  [optional] |



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
| UNKNOWN_DEFAULT_OPEN_API | &quot;unknown_default_open_api&quot; |



