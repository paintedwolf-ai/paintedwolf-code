### needs_decision resume

When a worker envelope or roster line shows **`needs_decision`**, read the structured decision first — `decision_request_json` on the envelope, or `last_worker_decision_request` in the host guidance block that reported it.

{% include "partials/coordinator-boundary-escalation.md" %}

When the coordinator is responsible for the choice: answer on the **same turn** with `answer_decision(job_id=<exact job_id from the envelope>, option=<number or exact option text>)`, then continue any independent coordination. Use **`wait(resume=true)`** when the next action needs that worker’s result; keep dependent work blocked until it completes.
