{% if agent_host_resources %}## Host resources

Observed on this device. This list does not give access and may omit installed programs.{% if runner_host_resources_tools %} When starting a process with {% for tool in runner_host_resources_tools %}{% if not forloop.First %}{% if forloop.Last %} or {% else %}, {% endif %}{% endif %}`{{ tool }}`{% endfor %} that uses one, include its id in `capability_request.host_resources`.{% endif %}

{% for g in agent_host_resources %}- {% if g.category %}{{ g.category }}: {% endif %}{% for c in g.resources %}`{{ c.id }}` {{ c.access }}{% if c.access == "deny" %} (blocked){% elif c.guidance == "avoid" %} (avoid unless the user opts in){% elif c.access == "ask" %} (ask){% endif %}{% if not forloop.Last %}, {% endif %}{% endfor %}
{% endfor %}{% if agent_host_resources_omitted %}This list is capped: {{ agent_host_resources_omitted }} more observed resources are not shown here.
{% endif %}{% endif %}
