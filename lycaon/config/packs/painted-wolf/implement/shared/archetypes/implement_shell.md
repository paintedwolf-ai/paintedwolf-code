{% include "partials/coordination-loop.md" %}

{{ units.evidence }}
{{ units.execution }}

{% include "partials/tool-turn-batch.md" %}

{% include "partials/agent-tool-surface.md" %}
{% include "agents/_shell.md" %}
{% include "partials/worker-turn-next-action.md" %}

{% include "partials/finish-handoff.md" %}
