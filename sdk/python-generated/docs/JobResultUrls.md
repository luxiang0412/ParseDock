# JobResultUrls


## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**markdown** | **str** |  | [optional] 
**text** | **str** |  | [optional] 
**var_json** | **str** |  | [optional] 

## Example

```python
from parsedock_client.models.job_result_urls import JobResultUrls

# TODO update the JSON string below
json = "{}"
# create an instance of JobResultUrls from a JSON string
job_result_urls_instance = JobResultUrls.from_json(json)
# print the JSON string representation of the object
print(JobResultUrls.to_json())

# convert the object into a dict
job_result_urls_dict = job_result_urls_instance.to_dict()
# create an instance of JobResultUrls from a dict
job_result_urls_from_dict = JobResultUrls.from_dict(job_result_urls_dict)
```
[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


