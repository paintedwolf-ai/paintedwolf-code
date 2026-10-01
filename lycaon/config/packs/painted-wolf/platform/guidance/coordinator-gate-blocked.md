Gate blocked — read `failed_leaves` in host context; do not mark the phase complete in chat. Fix evidence or delegate until gates pass.
{% if gate_obligations %}
### Phase obligations
{% for o in gate_obligations %}- `{{ o.id }}`: {{ o.purpose }}
{% if o.required %}  Required (exact ## headings — write these titles literally):
{% for r in o.required %}  - `{{ r }}`
{% endfor %}{% endif %}{% if o.missing %}{% for m in o.missing %}  - missing: {{ m }}
{% endfor %}{% endif %}{% for s in o.satisfy %}  {{ forloop.Counter }}. {{ s }}
{% endfor %}{% endfor %}{% endif %}
