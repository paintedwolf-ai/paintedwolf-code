## Landing overlays (`promote_overlay` / `reject_overlay` / `preview_overlay`)

Each finished write leg is a **revision-bound changeset** (`overlay_id` == `job_id`). Preserve wanted overlays as repair boundaries; land them one at a time in `pack_board` → `overlay_merge_plan.promote_sequence` order.

1. Inspect the envelope and current `source_evidence`. Validate the delivered behavior with appropriate inspection or checks. Stale receipts do not prove the current revision. Resume the same child for incomplete delivery, concrete defects, or useful missing checks; do not repeat blocked or unrelated failures merely to obtain a receipt.
2. Promote coherent, wanted work once reviewed, recording validation limits. Validation evidence is advisory for promotion; explicit workflow checks still require passing evidence on integrated source. Roughness alone is not a reason to reject.
3. `promote_overlay(overlay_id)` is atomic: unresolved conflicts land nothing. Inspect the menu and resolve every conflicting path together with `keep_both`, `keep_theirs`, or `keep_ours`; re-preview after primary changes.
4. `drop` only accidental/generated material, never to hide failed checks or unfinished work. Suggested task paths do not filter promotion; confinement remains the authority boundary.
5. `reject_overlay` is terminal: use it only for wrong, superseded, duplicate, or unwanted work. Preserve partial delivery and resume with `task(child_session_id=…)`. Do not redispatch already-delivered work.

Use `preview_overlay` for a closer review; verify the combined result as its behavior requires. Update progress as overlays land. `extend_worker_budget` is for running workers, not terminal legs.
{% include "partials/coordinator-needs-decision-resume.md" %}
