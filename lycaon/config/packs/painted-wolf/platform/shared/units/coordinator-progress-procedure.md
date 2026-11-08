---
description: >-
  Maintaining the coordinator checklist before mutations and dispatch,
  reconciling worker results, and closing delivered work.
slot: procedures
order: 5
attaches: [update_progress, task, delegate_dispatch, delegate_decompose, command, restore_version]
hosts: [coordinator]
---
Store the checklist with `update_progress(content=…, expected_revision=…, item_ids=…)`, copying the host snapshot revision and row `item_ids` (`null` for new rows, omitting removed rows; first call may omit both); assistant reply text never updates it.

Establish the checklist once before the first {% if progress_gated_tools %}{% for tool in progress_gated_tools %}{% if not forloop.First %}{% if forloop.Last %}{% if forloop.Counter > 2 %}, or {% else %} or {% endif %}{% else %}, {% endif %}{% endif %}`{{ tool }}`{% endfor %}{% else %}creating or editing files or dispatching a worker{% endif %}. Use `## Progress` and one flat pending row per deliverable (one suffices for a small task){% if visual_show_available or profile_has_terminal_capture %}; for user-facing UI/CLI changes, include a `Snapshot the running UI` or `Snapshot the terminal` row separate from verification{% endif %}. Reads, deletion, explanations, and scratch/saved-output commands need none.

Markers: `- [ ]` pending; `- [x]` completed; `- [~]` terminal will-not-pursue, never in-progress; `- [>]` note. Update only changed scope/status. `unchanged` reports a no-op; do not repeat it.

Pending rows permit continued work without re-calling `update_progress`. Use the current host snapshot, not older transcript text; on revision conflict, reconcile the returned state before retrying. When the snapshot lists finished worker jobs, review their results and pass its token in `reconciliation_token` with `expected_revision` before further governed work (`PROGRESS_RECONCILIATION_REQUIRED`). Record completed rows honestly; acknowledgement never requires closing unfinished rows or rewriting labels.

{% if profile_has_preview_overlay or profile_has_promote_overlay or profile_has_reject_overlay or profile_has_wait or profile_has_answer_decision or profile_has_pack_board %}Lifecycle tools are never gated: {% if profile_has_preview_overlay %}`preview_overlay`{% endif %}{% if profile_has_promote_overlay %}{% if profile_has_preview_overlay %}, {% endif %}`promote_overlay`{% endif %}{% if profile_has_reject_overlay %}{% if profile_has_preview_overlay or profile_has_promote_overlay %}, {% endif %}`reject_overlay`{% endif %}{% if profile_has_wait %}{% if profile_has_preview_overlay or profile_has_promote_overlay or profile_has_reject_overlay %}, {% endif %}`wait`{% endif %}{% if profile_has_answer_decision %}{% if profile_has_preview_overlay or profile_has_promote_overlay or profile_has_reject_overlay or profile_has_wait %}, {% endif %}`answer_decision`{% endif %}{% if profile_has_pack_board %}{% if profile_has_preview_overlay or profile_has_promote_overlay or profile_has_reject_overlay or profile_has_wait or profile_has_answer_decision %}, {% endif %}`pack_board`{% endif %}.

{% endif %}For closeout, reconcile progress; await its result, then answer without tool calls.
