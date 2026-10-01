Phase advanced to `{{ current_phase }}`. Treat the current workflow block as authoritative; do not continue instructions from an earlier phase. Workflow: `{{ workflow_id }}`.
{% if gate_obligations %}

### Phase obligations
{% for o in gate_obligations %}- `{{ o.id }}`: {{ o.purpose }}
{% if o.required %}  Required (exact ## headings — write these titles literally):
{% for r in o.required %}  - `{{ r }}`
{% endfor %}{% endif %}{% if o.missing %}{% for m in o.missing %}  - missing: {{ m }}
{% endfor %}{% endif %}{% for s in o.satisfy %}  {{ forloop.Counter }}. {{ s }}
{% endfor %}{% endfor %}{% endif %}
