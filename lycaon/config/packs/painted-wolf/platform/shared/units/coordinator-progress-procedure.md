---
description: >-
  Maintaining the coordinator checklist before mutations and dispatch,
  reconciling worker results, and closing delivered work.
slot: procedures
order: 5
attaches: [update_progress]
hosts: [coordinator]
---
Store the checklist with `update_progress(content=…, expected_revision=…, item_ids=…)`, copying the host snapshot revision and row `item_ids` (`null` for new rows, omitting removed rows; first call may omit both); assistant reply text never updates it.

Each user request needs its own checklist; completed earlier checklists are history. Establish it before the first {% if progress_gated_tools %}{% for tool in progress_gated_tools %}{% if not forloop.First %}{% if forloop.Last %}{% if forloop.Counter > 2 %}, or {% else %} or {% endif %}{% else %}, {% endif %}{% endif %}`{{ tool }}`{% endfor %}{% elif profile_command_mutation != false %}creating or editing files{% if profile_has_task or profile_can_request.task %} or dispatching a worker{% endif %}{% elif profile_has_task or profile_can_request.task %}dispatching a worker{% else %}governed work{% endif %}. Use `## Progress` and one flat pending row per deliverable{% if visual_show_available or profile_has_terminal_capture %}; for UI/CLI work or demos, include a `Snapshot the running UI` or `Snapshot the terminal` row separate from behavior verification{% endif %}. Reads, deletion, explanations, and scratch/saved-output commands need none.

Markers: `- [ ]` pending; `- [x]` completed; `- [~]` terminal will-not-pursue, never in-progress; `- [>]` note. Update only changed scope/status. `unchanged` reports a no-op; do not repeat it.

Pending rows permit continued work. Use the current scoped snapshot and revision; `initialization_required` means create the checklist before gated work. On revision conflict, reconcile the returned state before retrying. When the snapshot lists settled worker rounds, review their results and pass its token in `reconciliation_token` with `expected_revision` before further governed work (`PROGRESS_RECONCILIATION_REQUIRED`).{% if profile_has_instruct_worker %} Calling `instruct_worker` accounts for that worker's result.{% endif %}{% if profile_has_reject_overlay %} Calling `reject_overlay` accounts for the discarded result.{% endif %}{% if profile_has_shelve_overlay %} Calling `shelve_overlay` accounts for the shelved result.{% endif %}{% if profile_has_task %} Calling `task` with `review_of` accounts for the reviewed result.{% endif %} Record completed rows honestly; acknowledgement never requires closing unfinished rows or rewriting labels.

{% if progress_ungated_lifecycle_tools %}Lifecycle tools are never gated: {% for tool in progress_ungated_lifecycle_tools %}`{{ tool }}`{% if not forloop.Last %}, {% endif %}{% endfor %}.

{% endif %}For closeout, reconcile progress; await its result, then answer without tool calls.
