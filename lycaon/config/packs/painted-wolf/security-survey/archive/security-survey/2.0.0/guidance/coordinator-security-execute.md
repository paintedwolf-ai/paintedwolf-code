Execute the stamped fan-out plan — dispatch its legs, then wait.

{% if fanout_plan %}
**Plan:**
{{ fanout_plan }}
{% endif %}
{% if obligations and obligations.scan %}
**Scans:** `{{ obligations.scan.status }}`{% if obligations.scan.findings_count %} · {{ obligations.scan.findings_count }} finding(s){% endif %}{% if obligations.scan.error %} · {{ obligations.scan.error }}{% endif %}
{% endif %}

1. Dispatch every planned leg with `task(workflow_work_id)` in **one assistant turn**. The host supplies its stamped assignment, threat model, scan facts, agent, scope, and budget. Preserve each falsification question; supplementary observations do not replace host facts.
2. Call `wait(timeout_ms=1800000, conditions=[{"kind":"all_workers_idle"}])` — do not synthesize until every completion envelope lands. A leg that asks for more rounds wakes this wait; when one does, grant with `extend_worker_budget` if the remaining work it names serves its falsification question, otherwise `decline_worker_budget`, then wait again.
3. Recover incomplete legs within the plan's attempt allowance by resuming the same child: `task(child_session_id, brief)` keeps its leg, agent, and scope. Narrow the recovery brief to what remains; do not repeat successful legs or add new scouts. Exhausted legs remain coverage gaps. Do not `task(skeptic)` or `task(web-researcher)` here. Workers must not poll or follow ambient scans. Completed scans remain usable when coverage is partial. A finished worker job may have a partial evidence report; retain valid observations and recover only remaining work.

When every planned leg succeeds or exhausts its recovery allowance, the host advances automatically. Queue idleness alone is not completion. Do not call the phase-advance tool from coordinator prose.
