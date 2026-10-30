# CancelJobRequest


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**result_version** | **str** | Required when the job is already completed. | [optional] 

## Example

```python
from parsedock_client.models.cancel_job_request import CancelJobRequest

# TODO update the JSON string below
json = "{}"
# create an instance of CancelJobRequest from a JSON string
cancel_job_request_instance = CancelJobRequest.from_json(json)
# print the JSON string representation of the object
print(CancelJobRequest.to_json())

# convert the object into a dict
cancel_job_request_dict = cancel_job_request_instance.to_dict()
# create an instance of CancelJobRequest from a dict
cancel_job_request_from_dict = CancelJobRequest.from_dict(cancel_job_request_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


