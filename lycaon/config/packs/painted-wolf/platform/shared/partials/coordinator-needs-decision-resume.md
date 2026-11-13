### needs_decision resume

When a worker envelope or roster line shows **`needs_decision`**, read the structured decision first — `decision_request_json` on the envelope, or `last_worker_decision_request` in the host guidance block that reported it.

{% include "partials/coordinator-boundary-escalation.md" %}

When the coordinator is responsible for the choice: answer on the **same turn**{% if profile_has_answer_decision %} with `answer_decision(worker=<the asking worker's name>, answer=<an option's number or text, or your own words when it offered none>)`{% endif %}, then continue any independent coordination.{% if profile_has_wait %} Use **`wait(resume=true)`** when the next action needs that worker's result; keep dependent work blocked until it delivers.{% endif %}
