Plan one targeted drill wave from the scout evidence. This is the final exploration wave.

{% if topology_output %}
**Scout evidence:**
{{ topology_output }}
{% endif %}

1. Choose **1–{{ max_fanout_legs }}** read-only legs that resolve the material gaps named at reconciliation. Do not repeat broad orientation or inventory work.
2. Give each leg a concrete question, the discovered paths or boundary when known, the evidence it must return, and a finish condition. Deliberate overlap is allowed only to resolve a conflict or independently confirm an important claim.
3. Call `fanout_plan` with the legs (each with a plain `subject`), then `workflow_advance` when `fanout_planned` is satisfied.

Unknowns that do not affect the answer belong in the final report, not another leg.
