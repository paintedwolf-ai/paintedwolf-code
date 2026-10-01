{% if agent_host_resources %}## Host resources

Observed on this device. Not a grant and not a closed set — other binaries may exist. On a process start that uses one, declare its id in `capability_request.host_resources`.

{% for g in agent_host_resources %}- {% if g.category %}{{ g.category }}: {% endif %}{% for c in g.resources %}`{{ c.id }}` {{ c.access }}{% if c.access == "deny" %} (blocked){% elif c.guidance == "avoid" %} (avoid unless the user opts in){% elif c.access == "ask" %} (ask){% endif %}{% if not forloop.Last %}, {% endif %}{% endfor %}
{% endfor %}{% if agent_host_resources_omitted %}This list is capped: {{ agent_host_resources_omitted }} more observed resources are not shown here.
{% endif %}{% endif %}
