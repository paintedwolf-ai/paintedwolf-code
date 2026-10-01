Leg finished — review envelope and host digest; chain `task()` per policy.{% if progress_closure_armed %} This finish latched the checklist with **{{ progress_open_items }}** row(s) open: `update_progress` before that `task()`, or it returns `Code: PROGRESS_ITEM_NOT_CLOSED`.{% endif %}
{% if worker_digest %}
{{ worker_digest }}
{% endif %}
{% if profile_has_recall %}A detail the envelope omitted is still on record: `recall(query="{% if leg_id %}leg:{{ leg_id }} {% endif %}<term>")` returns what the leg observed — do not re-dispatch to see it again.
{% endif %}{% if advance_when_gate_met == "coordinator" %}Call `workflow_advance` when phase gates pass and you are ready to leave.
{% else %}No `workflow_advance` in build chat.{% endif %}
No implementation paste in coordinator prose.{% if completed_ago %} · {{ completed_ago }}{% endif %}
