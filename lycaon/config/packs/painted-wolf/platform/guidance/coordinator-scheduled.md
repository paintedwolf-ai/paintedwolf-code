Scheduled wake — your **timer** trigger fired.

{% if pending_overlays %}{{ pending_overlays }} result{% if pending_overlays != 1 %}s{% endif %} on the roster await{% if pending_overlays == 1 %}s{% endif %} your call: {% if profile_has_preview_overlay %}`preview_overlay` shows the changes, then land or reject them{% else %}decide their landing{% endif %}. Do not wait.
{% endif %}{% if gate_obligations %}Workflow **not finished** — these phase obligations are open; a delivered answer does not settle them. Continue now: dispatch needed workers, submit the required verdict, or wait on in-flight work.

### Phase obligations
{% for o in gate_obligations %}- `{{ o.id }}`: {{ o.purpose }}
{% if o.missing %}{% for m in o.missing %}  - missing: {{ m }}
{% endfor %}{% endif %}{% for s in o.satisfy %}  {{ forloop.Counter }}. {{ s }}
{% endfor %}{% endfor %}{% else %}Check roster and pack board.{% if batch_phase != "closed" %}{% if not pending_overlays %} Dispatch a worker when needed. Idle + answer delivered → prose only; do **not** re-arm a timer wait. Else wait while siblings fly — text only if cited.{% endif %}{% else %} Batch closed — do not wait; end the turn.{% endif %}{% endif %}
