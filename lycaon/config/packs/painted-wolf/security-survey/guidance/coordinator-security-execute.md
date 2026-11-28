Execute the stamped fan-out plan — dispatch its legs, then wait.

{% if fanout_plan %}
**Plan:**
{{ fanout_plan }}
{% endif %}
{% if obligations and obligations.scan %}
**Scans:** `{{ obligations.scan.status }}`{% if obligations.scan.findings_count %} · {{ obligations.scan.findings_count }} finding(s){% endif %}{% if obligations.scan.error %} · {{ obligations.scan.error }}{% endif %}
{% endif %}

1. Dispatch every planned leg{% if profile_has_task %} with `task(workflow_work_id)`{% else %} by its `workflow_work_id`{% endif %} in **one assistant turn**. The host supplies its stamped assignment, threat model, scan facts, agent, and scope. Preserve each falsification question; supplementary observations do not replace host facts.
2. {% if profile_has_wait %}Call `wait(timeout_ms=1800000, conditions=[{"kind":"all_workers_idle"}])` — do{% else %}Do{% endif %} not synthesize until every completion envelope lands.{% if profile_has_wait %} A round notice about a long or quiet leg also wakes this wait;{% if profile_has_worker_cancel %} stop that leg with `worker_cancel` only if its remaining work no longer serves its falsification question, otherwise wait again.{% else %} wait again.{% endif %}{% endif %}
3. Recover incomplete legs within the plan's attempt allowance{% if profile_has_instruct_worker %} by sending the same worker back with `instruct_worker`: it keeps its leg, agent, scope and what it already found{% endif %}. Narrow the recovery to what remains; do not repeat successful legs or add new scouts. Exhausted legs remain coverage gaps. {% if profile_has_task %}Do not `task(skeptic)` or `task(web-researcher)` here.{% else %}Do not start skeptic or web-researcher legs here.{% endif %} Workers must not poll or follow ambient scans. Completed scans remain usable when coverage is partial. A finished worker job may have a partial evidence report; retain valid observations and recover only remaining work.

When every planned leg succeeds or exhausts its recovery allowance, the host advances automatically. Queue idleness alone is not completion. Do not call the phase-advance tool from coordinator prose.
