

# Job


## Properties

| Name | Type | Description | Notes |
|------------ | ------------- | ------------- | -------------|
|**jobId** | **String** |  |  |
|**status** | [**StatusEnum**](#StatusEnum) | Processing dimension. |  |
|**processingStatus** | **String** | Alias of &#x60;status&#x60; (both are returned). |  [optional] |
|**retentionStatus** | [**RetentionStatusEnum**](#RetentionStatusEnum) | Retention dimension, independent of &#x60;status&#x60;. |  |
|**inputName** | **String** |  |  [optional] |
|**format** | **String** |  |  [optional] |
|**mimeType** | **String** |  |  [optional] |
|**inputSize** | **Long** |  |  [optional] |
|**inputSha256** | **String** |  |  [optional] |
|**pageCount** | **Integer** | Engine-reported when available |  [optional] |
|**createdAt** | **OffsetDateTime** |  |  |
|**startedAt** | **OffsetDateTime** |  |  [optional] |
|**completedAt** | **OffsetDateTime** |  |  [optional] |
|**updatedAt** | **OffsetDateTime** |  |  [optional] |
|**resultExpiresAt** | **OffsetDateTime** | TTL measured from result commit, not upload. |  [optional] |
|**processingDeadlineAt** | **OffsetDateTime** | Deadline for **queued/running** work only (created + max_job_lifetime). It does not bound a completed result: retention is governed by &#x60;result_expires_at&#x60;.  |  [optional] |
|**maxLifetimeAt** | **OffsetDateTime** | Deprecated alias of &#x60;processing_deadline_at&#x60; (same value). |  [optional] |
|**attempt** | **Integer** |  |  [optional] |
|**requestedMode** | **String** |  |  [optional] |
|**requestedDevice** | **String** |  |  [optional] |
|**engine** | **String** | Engine that actually produced the result |  [optional] |
|**routingReason** | **String** | Human-readable routing decision |  [optional] |
|**progressPercent** | **Integer** |  |  [optional] |
|**progressStage** | **String** |  |  [optional] |
|**cancelRequestedAt** | **OffsetDateTime** | Set when cancellation was requested (queued/running jobs). The worker stops at its next heartbeat (default interval 10 s) and finalises the attempt; the job is already &#x60;cancelled&#x60; when this field appears.  |  [optional] |
|**resultVersion** | **String** | Manifest hash; echo it in the ACK (&#x60;X-Result-Version&#x60;). |  [optional] |
|**resultUrls** | [**JobResultUrls**](JobResultUrls.md) |  |  [optional] |
|**warningCount** | **Integer** |  |  [optional] |
|**errorCode** | **String** |  |  [optional] |
|**errorMessage** | **String** |  |  [optional] |
|**statusUrl** | **String** |  |  |
|**eventsUrl** | **String** |  |  [optional] |
|**ackUrl** | **String** |  |  [optional] |
|**cancelUrl** | **String** |  |  [optional] |



## Enum: StatusEnum

| Name | Value |
|---- | -----|
| QUEUED | &quot;queued&quot; |
| RUNNING | &quot;running&quot; |
| COMPLETED | &quot;completed&quot; |
| FAILED | &quot;failed&quot; |
| CANCELLED | &quot;cancelled&quot; |
| UNKNOWN_DEFAULT_OPEN_API | &quot;unknown_default_open_api&quot; |



## Enum: RetentionStatusEnum

| Name | Value |
|---- | -----|
| ACTIVE | &quot;active&quot; |
| RELEASE_PENDING | &quot;release_pending&quot; |
| EXPIRY_PENDING | &quot;expiry_pending&quot; |
| DELETING | &quot;deleting&quot; |
| RELEASED | &quot;released&quot; |
| EXPIRED | &quot;expired&quot; |
| UNKNOWN_DEFAULT_OPEN_API | &quot;unknown_default_open_api&quot; |



