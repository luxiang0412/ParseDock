# AckJob202Response


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**job_id** | **str** |  | [optional] 
**retention_status** | **str** |  | [optional] 
**result_version** | **str** |  | [optional] 
**accepted_at** | **datetime** |  | [optional] 
**message** | **str** |  | [optional] 

## Example

```python
from parsedock_client.models.ack_job202_response import AckJob202Response

# TODO update the JSON string below
json = "{}"
# create an instance of AckJob202Response from a JSON string
ack_job202_response_instance = AckJob202Response.from_json(json)
# print the JSON string representation of the object
print(AckJob202Response.to_json())

# convert the object into a dict
ack_job202_response_dict = ack_job202_response_instance.to_dict()
# create an instance of AckJob202Response from a dict
ack_job202_response_from_dict = AckJob202Response.from_dict(ack_job202_response_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


