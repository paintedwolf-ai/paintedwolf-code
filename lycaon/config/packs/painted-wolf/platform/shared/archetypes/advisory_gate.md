{% include "partials/coordination-loop.md" %}

{% if units.orientation %}
## Survey tools

{{ units.orientation }}
{% endif %}

{% include "partials/agent-tool-surface.md" %}
{{ units.evidence }}
{{ units.execution }}
{% include "partials/advisory-vs-gates.md" %}
{% include "agents/_shell.md" %}
{% include "partials/finish-handoff.md" %}
