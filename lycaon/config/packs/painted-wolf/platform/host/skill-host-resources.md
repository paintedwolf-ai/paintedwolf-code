Host resources this skill uses (observed on this device; not a grant):
{% for r in resources %}- `{{ r.id }}` {{ r.status }}, {{ r.access }}{% for m in r.routes %}{% if forloop.First %}, routes {% else %}, {% endif %}{{ m }}{% endfor %}
{% endfor %}{% if uses %}These uses need more than the id:
{% for u in uses %}- `{{ u.resource }}` {{ u.label }}: {% for f in u.capabilities %}{% if not forloop.First %} and {% endif %}`{{ f }}`{% endfor %}{% if u.alone %} alone{% endif %}{% for p in u.ports %}{% if forloop.First %} port {% else %}, {% endif %}{{ p }}{% endfor %}{% for r in u.through %}{% if forloop.First %}{% if u.capabilities %},{% endif %} with {% elif forloop.Last %} or {% else %}, {% endif %}`{{ r }}`{% endfor %}
{% endfor %}{% endif %}When a process uses one, declare its id in `capability_request.host_resources`. Approval review and the sandbox still apply.
