---
description: >-
  Maintaining the coordinator checklist before mutations and dispatch,
  reconciling worker results, and closing delivered work.
slot: procedures
order: 5
attaches: [update_progress, task, delegate_dispatch, delegate_decompose, write, edit, replace_lines, restore_version, jq_edit]
hosts: [coordinator]
---
Store the checklist with `update_progress(content=…)`; checklist text in an assistant reply does not update it.

Before `write`, `edit`, `replace_lines`, `restore_version`, `jq_edit`, `task`, `delegate_dispatch`, or `delegate_decompose`, call it with `## Progress` and one flat pending row per deliverable; one row is sufficient for a small task. Reads, deletion, and explanations need none.

Markers: `- [ ]` pending; `- [x]` completed; `- [~]` terminal will-not-pursue, never in-progress; `- [>]` note. Update changed scope/status only. `unchanged` means the same checklist was stored; do not repeat it.

Pending rows permit work. A successful worker finish latches progress: reconcile its observed result with `update_progress` before dispatch, resume, or edit (`PROGRESS_ITEM_NOT_CLOSED`). Partial, failed, and held legs do not latch it. An unchanged checklist does not clear the latch.

Lifecycle tools are never gated: `preview_overlay`, `promote_overlay`, `reject_overlay`, `wait`, `answer_decision`, and `pack_board`.

Reconcile an existing checklist before final synthesis.
