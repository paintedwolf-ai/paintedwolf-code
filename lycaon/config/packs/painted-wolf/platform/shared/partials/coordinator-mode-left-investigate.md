## Left investigate

{% if execution_mode == "orchestrate" %}Continue through the current coordination tools: {% if profile_has_task %}use `task` for remaining independent execution work; {% endif %}{% if profile_has_wait %}use `wait` with a worker-result condition for a dependency on running work. {% endif %}Follow the current surface card. Do not retry earlier inline calls or keep execution work queued for tools to return.{% else %}Follow the current surface card for the remaining work; earlier inline calls do not establish tool availability.{% endif %}
{% include "partials/execution-mode-supersession.md" %}
