# AckJobRequest


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**result_version** | **str** | Manifest hash obtained from the download. Required for the first ACK. | 
**action** | **str** |  | [optional] [default to 'release']

## Example

```python
from parsedock_client.models.ack_job_request import AckJobRequest

# TODO update the JSON string below
json = "{}"
# create an instance of AckJobRequest from a JSON string
ack_job_request_instance = AckJobRequest.from_json(json)
# print the JSON string representation of the object
print(AckJobRequest.to_json())

# convert the object into a dict
ack_job_request_dict = ack_job_request_instance.to_dict()
# create an instance of AckJobRequest from a dict
ack_job_request_from_dict = AckJobRequest.from_dict(ack_job_request_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


