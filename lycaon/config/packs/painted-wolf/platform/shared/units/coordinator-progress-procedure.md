---
description: >-
  Maintaining the coordinator checklist before mutations and dispatch,
  reconciling worker results, and closing delivered work.
slot: procedures
order: 5
attaches: [update_progress, task, delegate_dispatch, delegate_decompose, write, edit, replace_lines, restore_version, jq_edit]
hosts: [coordinator]
---
Store the checklist with `update_progress(content=…)`, not assistant prose.

Before `write`, `edit`, `replace_lines`, `restore_version`, `jq_edit`,{% if profile_has_delegate_dispatch %} `task`,{% if profile_has_delegate_decompose %} `delegate_dispatch`, or `delegate_decompose`{% else %} or `delegate_dispatch`{% endif %}{% elif profile_has_delegate_decompose %} `task`, or `delegate_decompose`{% else %} or `task`{% endif %}, call it with `## Progress` and one flat pending row per deliverable; one row is sufficient for a small task. Reads, deletion, and explanations need none.

Markers: `- [ ]` pending; `- [x]` done; `- [~]` will-not-pursue, never in-progress; `- [>]` note. Update only changed scope/status. Do not repeat an `unchanged` result.

{% if progress_just_repaired %}The latest checklist update did not execute `prior_unexecuted_calls`. If still needed, retry their original calls through normal tool admission.{% endif %}

Pending rows permit work. A successful worker finish latches progress: reconcile its observed result with `update_progress` before dispatch, resume, or edit (`PROGRESS_ITEM_NOT_CLOSED`). Partial/failed/held legs do not latch it; unchanged checklists do not clear it.

Lifecycle tools are never gated: `preview_overlay`, `promote_overlay`, `reject_overlay`, `wait`, `answer_decision`, and `pack_board`.

Reconcile an existing checklist before final synthesis.
