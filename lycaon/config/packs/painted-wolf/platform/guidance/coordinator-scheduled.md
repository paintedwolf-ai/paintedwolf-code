Scheduled wake — your `wait()` **timer** trigger fired.

{% if pending_overlay_jobs %}Pending overlays {% for id in pending_overlay_jobs %}`{{ id }}`{% if not forloop.Last %}, {% endif %}{% endfor %}: `preview_overlay` then `promote_overlay` or `reject_overlay` — do not wait.
{% endif %}{% if gate_obligations %}Workflow **not finished** — these phase obligations are open; a delivered answer does not settle them. Continue now: dispatch `task()`, submit the required verdict, or `wait(timeout_ms=…, conditions=[…])` on in-flight work.

### Phase obligations
{% for o in gate_obligations %}- `{{ o.id }}`: {{ o.purpose }}
{% if o.missing %}{% for m in o.missing %}  - missing: {{ m }}
{% endfor %}{% endif %}{% for s in o.satisfy %}  {{ forloop.Counter }}. {{ s }}
{% endfor %}{% endfor %}{% else %}Check roster and pack board.{% if batch_phase != "closed" %}{% if not pending_overlay_jobs %} Dispatch `task()` when needed. Idle + answer delivered → prose only; do **not** re-arm a timer wait. Else **`wait(timeout_ms=…, conditions=[…])`** or **`wait(resume=true)`** if siblings fly — no prose.{% endif %}{% else %} Batch closed — do not call `wait()`; end the turn.{% endif %}{% endif %}
