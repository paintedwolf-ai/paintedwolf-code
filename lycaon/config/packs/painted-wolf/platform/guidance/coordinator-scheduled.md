Scheduled wake — your **timer** trigger fired.

{% if pending_overlay_jobs %}Pending overlays {% for id in pending_overlay_jobs %}`{{ id }}`{% if not forloop.Last %}, {% endif %}{% endfor %}: {% if profile_has_preview_overlay %}`preview_overlay`, then promote or reject{% else %}decide their landing{% endif %} — do not wait.
{% endif %}{% if gate_obligations %}Workflow **not finished** — these phase obligations are open; a delivered answer does not settle them. Continue now: dispatch needed workers, submit the required verdict, or wait on in-flight work.

### Phase obligations
{% for o in gate_obligations %}- `{{ o.id }}`: {{ o.purpose }}
{% if o.missing %}{% for m in o.missing %}  - missing: {{ m }}
{% endfor %}{% endif %}{% for s in o.satisfy %}  {{ forloop.Counter }}. {{ s }}
{% endfor %}{% endfor %}{% else %}Check roster and pack board.{% if batch_phase != "closed" %}{% if not pending_overlay_jobs %} Dispatch a worker when needed. Idle + answer delivered → prose only; do **not** re-arm a timer wait. Else wait while siblings fly — text only if cited.{% endif %}{% else %} Batch closed — do not wait; end the turn.{% endif %}{% endif %}
