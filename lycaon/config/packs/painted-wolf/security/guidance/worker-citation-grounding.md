[host:worker-citation-grounding]

Call **`complete_leg`** again with the full report, fixing what the rejection lists. Put handles in `evidence`, never as `[kind#n]` tags in narrative.
Copy excerpts from an observation, never memory; {% if can_recall %}recover one with `recall(query="handle:read#3")` or a `path:` query.{% else %}re-observe it with an offered tool.{% endif %}

{% include "partials/worker-complete-leg.md" %}
