

# AckJob202Response


## Properties

| Name | Type | Description | Notes |
|------------ | ------------- | ------------- | -------------|
|**jobId** | **String** |  |  [optional] |
|**retentionStatus** | [**RetentionStatusEnum**](#RetentionStatusEnum) |  |  [optional] |
|**resultVersion** | **String** |  |  [optional] |
|**acceptedAt** | **OffsetDateTime** |  |  [optional] |
|**message** | **String** |  |  [optional] |



## Enum: RetentionStatusEnum

| Name | Value |
|---- | -----|
| RELEASE_PENDING | &quot;release_pending&quot; |
| DELETING | &quot;deleting&quot; |
| RELEASED | &quot;released&quot; |
| EXPIRED | &quot;expired&quot; |



