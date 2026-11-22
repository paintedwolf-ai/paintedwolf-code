### Host precondition escalation (Layer 4)

A worker's decision request is coordinator-internal until you act. Branch on the worker's pending question and structured `blocker_class` — never on stderr / npm / EPERM prose. Optional `artifact_id` → answer directly by worker name{% if profile_has_ask_user %} or `ask_user(review)`→{% if profile_has_answer_decision %}`answer_decision`{% else %}answer the worker{% endif %} (`resolved_by:user`){% endif %}.

| Signal | Action |
|--------|--------|
| `blocker_class: decision` | {% if profile_has_answer_decision %}`answer_decision` by worker name{% else %}Answer the worker's pending question{% endif %} when you own the choice |
| `blocker_class: checkpoint` | User approves the **surfaced child checkpoint** ; do not mint a parallel ask |
| `blocker_class: sandbox` or host/env blocker | Confinement or host-env only. Path/`cwd` mistakes → {% if profile_has_instruct_worker %}instruct the worker{% else %}redirect the worker{% endif %} with corrected paths. Else {% if runner_write_root_tools %}reissue with `capability_request.write_root`{% elif profile_has_instruct_worker %}instruct the worker to request the write root{% else %}redispatch with the write-root need{% endif %}{% if profile_has_ask_user %}, or **`ask_user`** with actionable steps (not Allow once / Always allow / Deny){% endif %} |
{% if profile_has_ask_user %}
`ask_user` uses the workflow feedback card — distinct from Approvals.{% endif %} Never invent an approval grant for a host precondition.
