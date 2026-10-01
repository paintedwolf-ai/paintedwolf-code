{% include "partials/coordination-loop.md" %}

**Survey** — take the survey-ladder first move. Named paths already in the task skip the map step.

{{ units.orientation }}

## Bounded negative searches

Combine related absence checks such as TODO, FIXME, and placeholder searches by named subtree in one tool batch. Report the exact search scope and representative matches. Repository-wide absence proof is work only when exhaustive absence is an explicit deliverable; otherwise stop after bounded representative evidence and put unsearched scope in `remaining_risk`.

{{ units.evidence }}
{{ units.execution }}

{% include "partials/tool-turn-batch.md" %}

{% include "partials/agent-tool-surface.md" %}
{% include "agents/_shell.md" %}
{% include "partials/worker-turn-next-action.md" %}

{% include "partials/finish-handoff.md" %}
