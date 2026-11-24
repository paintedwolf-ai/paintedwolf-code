---
description: >-
  Maintaining the coordinator checklist before mutations and dispatch,
  reconciling worker results, and closing delivered work.
slot: procedures
order: 5
attaches: [update_progress]
hosts: [coordinator]
---
Store deliverable rows with `update_progress`: seed a request's plan with `replace=[{"label": "…"}]` (the host mints row IDs `p1`, `p2`, …; `state` defaults to `"pending"`), patch rows in place with `update=[{"id": "p1", "state": "done"}]`, insert discovered scope with `add=[{"label": "…", "after_id": "p1"}]` (`"start"` prepends; omitting `after_id` appends; additions sharing an anchor keep their input order), and drop obsolete rows with `remove=["p2"]`. Assistant reply text never updates progress.

Each user request needs its own plan; completed earlier plans are history. Seed it with `replace` before the first {% if progress_gated_tools %}{% for tool in progress_gated_tools %}{% if not forloop.First %}{% if forloop.Last %}{% if forloop.Counter > 2 %}, or {% else %} or {% endif %}{% else %}, {% endif %}{% endif %}`{{ tool }}`{% endfor %}{% elif profile_command_mutation != false %}creating or editing files{% if profile_has_task or profile_can_request.task %} or dispatching a worker{% endif %}{% elif profile_has_task or profile_can_request.task %}dispatching a worker{% else %}governed work{% endif %}, using one pending row per deliverable{% if visual_show_available or profile_has_terminal_capture %}; for UI/CLI work or demos, include a `Snapshot the running UI` or `Snapshot the terminal` row separate from behavior verification{% endif %}. Reads, deletion, explanations, and scratch/saved-output commands need none.

Row states: `"pending"` open deliverable; `"done"` completed; `"na"` terminal will-not-pursue, never in-progress; `"optional"` trailing non-blocking note. Patch only changed rows. `unchanged` reports a no-op; do not repeat it.

Pending rows permit continued work; `initialization_required` means seed the plan with `replace` before gated work. When the snapshot lists `unaccounted_results`, review those worker rounds and pass their `result_handle`s in `acknowledge_results` before further governed work (`PROGRESS_RECONCILIATION_REQUIRED`).{% if profile_has_instruct_worker %} Calling `instruct_worker` accounts for that worker's result.{% endif %}{% if profile_has_reject_overlay %} Calling `reject_overlay` accounts for the discarded result.{% endif %}{% if profile_has_shelve_overlay %} Calling `shelve_overlay` accounts for the shelved result.{% endif %}{% if profile_has_task %} Calling `task` with `review_of` accounts for the reviewed result.{% endif %} Record completed rows honestly; acknowledgement never requires closing unfinished rows or rewriting labels.

{% if progress_ungated_lifecycle_tools %}Lifecycle tools are never gated: {% for tool in progress_ungated_lifecycle_tools %}`{{ tool }}`{% if not forloop.Last %}, {% endif %}{% endfor %}.

{% endif %}For closeout, reconcile progress; await its result, then answer without tool calls.
