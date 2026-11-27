{% if agent_host_resources %}## Host resources

Observed on this device. This list does not give access and may omit installed programs; unmarked ids are allowed.{% if (profile_has_command and profile_command_external_exec != false) or profile_has_verify or profile_has_terminal_open %} When starting a process that uses one, include its id in `capability_request.host_resources`.{% if host_unconfined %} This device has no process sandbox, so commands run only with `capability_request.host_execution`.{% endif %}{% endif %}

{% for g in agent_host_resources %}- {% if g.category %}{{ g.category }}: {% endif %}{% for c in g.resources %}`{{ c.id }}`{% if c.access == "deny" %} (blocked){% elif c.guidance == "avoid" %} (avoid unless the user opts in){% elif c.access == "ask" %} (ask){% endif %}{% if not forloop.Last %}, {% endif %}{% endfor %}
{% endfor %}{% if agent_host_resource_uses %}
These uses need more than the id:
{% for u in agent_host_resource_uses %}- `{{ u.resource }}` {{ u.label }}: {% for f in u.capabilities %}{% if not forloop.First %} and {% endif %}`{{ f }}`{% endfor %}{% if u.alone %} alone{% endif %}{% for p in u.ports %}{% if forloop.First %} port {% else %}, {% endif %}{{ p }}{% endfor %}{% for r in u.through %}{% if forloop.First %}{% if u.capabilities %},{% endif %} with {% elif forloop.Last %} or {% else %}, {% endif %}`{{ r }}`{% endfor %}
{% endfor %}{% endif %}{% if agent_host_resources_omitted %}This list is capped: {{ agent_host_resources_omitted }} more observed resources are not shown here.
{% endif %}{% endif %}
