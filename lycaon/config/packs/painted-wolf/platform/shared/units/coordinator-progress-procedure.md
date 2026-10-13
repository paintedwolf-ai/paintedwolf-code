---
description: >-
  Maintaining the coordinator checklist before mutations and dispatch,
  reconciling worker results, and closing delivered work.
slot: procedures
order: 5
attaches: [update_progress, task, delegate_dispatch, delegate_decompose, write, edit, replace_lines, restore_version, jq_edit]
hosts: [coordinator]
---
Maintain session progress with `update_progress(content=…)`; checklist text in an assistant reply does not update it.

Before `write`, `edit`, `replace_lines`, `restore_version`, `jq_edit`, `task`, `delegate_dispatch`, or `delegate_decompose`, submit a checklist headed `## Progress`: one flat `- [ ]` row per deliverable, with one row sufficient for a small task. Name deliverables from the request and known facts; survey only when needed to identify them. Reads, deletion, and explanation-only work do not require a checklist. Use the schema's line and label limits.

Keep work pending while implementing. Mark `- [x]` only after its result succeeds; a rejected action did not do the work. `- [~]` means terminal will-not-pursue, never in-progress; `- [>]` is a note. Change the checklist only when scope or status changes. `unchanged` is successful storage of the same checklist, not a reason to call again.

After a successful worker finish latches the checklist, call `update_progress` to reconcile the observed result before another dispatch, resume, or edit (`PROGRESS_ITEM_NOT_CLOSED`). Partial, failed, and held legs do not latch it. Lifecycle tools remain available: `preview_overlay`, `promote_overlay`, `reject_overlay`, `wait`, `answer_decision`, and `pack_board`.

For an existing checklist, reconcile completed work with `update_progress` before final synthesis. Continue unfinished work unless a concrete blocker or authorized scope change requires stopping. A response without tool calls ends the turn; it cannot start or continue work.
