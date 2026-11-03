---
description: >-
  Maintaining the coordinator checklist before mutations and dispatch,
  reconciling worker results, and closing delivered work.
slot: procedures
order: 5
attaches: [update_progress, task, delegate_dispatch, delegate_decompose, command, edit, replace_lines, restore_version, jq_edit]
hosts: [coordinator]
---
Store the checklist with `update_progress(content=…, expected_revision=…, item_ids=…)`, copying the current host snapshot revision and row `item_ids` (`null` for newly added rows, omitting removed rows; first creation may omit both); checklist text in an assistant reply does not update it.

Establish the checklist once before the first {% if progress_gated_tools %}{% for tool in progress_gated_tools %}{% if not forloop.First %}{% if forloop.Last %}{% if forloop.Counter > 2 %}, or {% else %} or {% endif %}{% else %}, {% endif %}{% endif %}`{{ tool }}`{% endfor %}{% else %}creating or editing files or dispatching a worker{% endif %}. Use `## Progress` and one flat pending row per deliverable; one row is sufficient for a small task. Reads, deletion, explanations, and commands that only write session scratch or saved output files need none.

Markers: `- [ ]` pending; `- [x]` completed; `- [~]` terminal will-not-pursue, never in-progress; `- [>]` note. Update only changed scope/status. `unchanged` reports a no-op; do not repeat it.

Pending rows permit continued work without another checklist call. Use the current host snapshot, not an older transcript checklist. A revision conflict returns current state; reconcile it before resubmitting. When the snapshot lists finished worker jobs, review their results and copy its reconciliation token into `reconciliation_token` with `expected_revision` on the progress update before further governed work (`PROGRESS_RECONCILIATION_REQUIRED`). Record completed deliverables honestly; acknowledgement does not require closing unfinished rows or rewriting labels. Document changes and worker-finish acknowledgement are separate outcomes.

{% if profile_has_preview_overlay or profile_has_promote_overlay or profile_has_reject_overlay or profile_has_wait or profile_has_answer_decision or profile_has_pack_board %}Lifecycle tools are never gated: {% if profile_has_preview_overlay %}`preview_overlay`{% endif %}{% if profile_has_promote_overlay %}{% if profile_has_preview_overlay %}, {% endif %}`promote_overlay`{% endif %}{% if profile_has_reject_overlay %}{% if profile_has_preview_overlay or profile_has_promote_overlay %}, {% endif %}`reject_overlay`{% endif %}{% if profile_has_wait %}{% if profile_has_preview_overlay or profile_has_promote_overlay or profile_has_reject_overlay %}, {% endif %}`wait`{% endif %}{% if profile_has_answer_decision %}{% if profile_has_preview_overlay or profile_has_promote_overlay or profile_has_reject_overlay or profile_has_wait %}, {% endif %}`answer_decision`{% endif %}{% if profile_has_pack_board %}{% if profile_has_preview_overlay or profile_has_promote_overlay or profile_has_reject_overlay or profile_has_wait or profile_has_answer_decision %}, {% endif %}`pack_board`{% endif %}.

{% endif %}For closeout, reconcile progress; await its result, then answer without tool calls.
