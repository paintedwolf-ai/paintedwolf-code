Execute the stamped fan-out wave — dispatch all legs in one batch, then wait for completion.

{% if fanout_plan %}
**Plan:**
{{ fanout_plan }}
{% endif %}

1. Dispatch every leg with `task(agent_type, brief, workflow_work_id, scope.mode: read, …)` in **one assistant turn** (batch dispatch). Use the host-issued leg ID as `workflow_work_id`.
2. Call `wait(timeout_ms=1800000, conditions=[{"kind":"all_workers_idle"}])` — do not synthesize until every completion envelope lands.
3. Do **not** add unstamped legs while this wave is running; the next phase adapts.

When every planned leg is complete or has exhausted its declared attempt allowance, the host advances automatically. An incomplete leg remains a coverage gap. If another attempt is allowed, keep its `workflow_work_id` and recover only that leg. Do not call the phase-advance tool from coordinator prose.
