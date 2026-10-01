>>> In-flight workers (batch dispatch)
{% for worker in workers %}- {{ worker.job_prefix }} · {{ worker.agent }} · {{ worker.status }} · {{ worker.scope_summary }}{% if worker.touch_summary %} · {{ worker.touch_summary }}{% endif %}
{% endfor %}The roster shows accepted work. Dispatch more only when eligible work and remaining capacity are known; preserve accepted legs if another dispatch is rejected.
Do not poll workers — wait for completion envelope(s) and pulse roster.
Code: BANNER_WORKER_INFLIGHT_ROSTER
