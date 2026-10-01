>>> Worker queued
{% if agent_type %}Agent: {{ agent_type }}
{% endif %}{% if job_id %}Job: {{ job_id }}
{% endif %}Worker accepted; completion is pending. Consider dispatching companion workers for independent sub-tasks, verification, or tests within the {{ max_in_flight }} worker cap, or `wait` when the next action requires a result. Read the completion envelope before claiming that leg is done. Retry only rejected dispatches.
Code: BANNER_TASK_QUEUED
