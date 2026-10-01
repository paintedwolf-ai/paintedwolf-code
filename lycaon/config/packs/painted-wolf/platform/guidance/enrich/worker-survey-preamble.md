## Synthesized survey

{% if agent_type == "path-explorer" %}Paths: (compiled from tool output below)
Unknowns: Worker did not emit a final prose turn; host compiled tool excerpts.
Summary: See tool excerpts.
{% elif agent_type == "repo-researcher" %}Lockfile / versions, project structure, facts, and unknowns: compiled from tool excerpts below.
Unknowns: Worker did not emit a final prose turn.
{% else %}Findings compiled from worker tool output (no final prose turn from the worker).
{% endif %}
