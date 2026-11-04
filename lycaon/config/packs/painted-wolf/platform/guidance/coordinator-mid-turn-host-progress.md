Worker status from the host:
{% for worker in workers %}- {{ worker.agent_type }}: {{ worker.status }}
{% endfor %}
