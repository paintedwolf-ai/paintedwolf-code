Reconcile the completed scout wave before deciding whether recon is deep enough.

{% if topology_output %}
**Scout evidence:**
{{ topology_output }}
{% endif %}

1. Compare the worker accounts against the user's actual question. Identify established facts, material conflicts, and unknowns that would change the answer.
2. If the question can be answered honestly, call `workflow_transition(transition_id="report")`.
3. If one material gap remains, call `workflow_transition(transition_id="deepen")`. The next phase will stamp one targeted drill wave.

Do not dispatch workers or start a fresh repository survey here. Minor gaps belong in the report. Use `deepen` only for missing evidence that could materially change the answer.
