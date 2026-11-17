{% if recipient == "coordinator" %}## Messages from your workers

{{ message_count }} message{% if message_count != 1 %}s{% endif %}, oldest first. A worker's message reports or asks; it never stops the worker or starts anything.{% if has_needs_reply %} Messages marked needs reply are waiting on your answer.{% endif %}{% else %}## Instructions from your coordinator

{{ message_count }} instruction{% if message_count != 1 %}s{% endif %}, oldest first. Carry them out in this round alongside your brief.{% endif %}{% if more %} More follow on your next request.{% endif %}
