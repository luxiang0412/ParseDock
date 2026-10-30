

# DocumentIR

`doc-ir/0.1`. Missing provenance (page, bbox, sheet, confidence) is absent, never fabricated. See docs/DOCUMENT-IR.md. 

## Properties

| Name | Type | Description | Notes |
|------------ | ------------- | ------------- | -------------|
|**schemaVersion** | [**SchemaVersionEnum**](#SchemaVersionEnum) |  |  |
|**parserEngine** | **String** |  |  |
|**parserVersion** | **String** |  |  [optional] |
|**routing** | [**DocumentIRRouting**](DocumentIRRouting.md) |  |  [optional] |
|**source** | [**DocumentIRSource**](DocumentIRSource.md) |  |  [optional] |
|**pageCount** | **Integer** |  |  [optional] |
|**pageKind** | [**PageKindEnum**](#PageKindEnum) |  |  [optional] |
|**elements** | [**List&lt;Element&gt;**](Element.md) |  |  |
|**tables** | [**List&lt;Table&gt;**](Table.md) |  |  [optional] |
|**warnings** | [**List&lt;DocumentIRWarningsInner&gt;**](DocumentIRWarningsInner.md) |  |  |
|**metrics** | [**DocumentIRMetrics**](DocumentIRMetrics.md) |  |  |



## Enum: SchemaVersionEnum

| Name | Value |
|---- | -----|
| DOC_IR_0_1 | &quot;doc-ir/0.1&quot; |



## Enum: PageKindEnum

| Name | Value |
|---- | -----|
| PDF | &quot;pdf&quot; |
| SHEET | &quot;sheet&quot; |
| SLIDE | &quot;slide&quot; |
| NONE | &quot;none&quot; |



