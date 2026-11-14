## Landing results

Each finished write round is a **revision-bound changeset** addressed by its result handle (`overlay_id`), which the roster shows. Preserve wanted results as repair boundaries; land them one at a time{% if profile_has_pack_board %} in `pack_board` → `overlay_merge_plan.promote_sequence` order{% endif %}.

1. Inspect the envelope and current `source_evidence`. Validate the delivered behavior with appropriate inspection or checks. Stale receipts do not prove the current revision. Send the worker back for incomplete delivery, concrete defects, or useful missing checks{% if profile_has_instruct_worker %} with `instruct_worker`{% endif %}; do not repeat blocked or unrelated failures merely to obtain a receipt.
2. Promote coherent, wanted work once reviewed, recording validation limits. Validation evidence is advisory for promotion; explicit workflow checks still require passing evidence on integrated source. Roughness alone is not a reason to reject.
3. {% if profile_has_promote_overlay %}`promote_overlay(overlay_id)` is atomic{% else %}Landing is atomic{% endif %}: unresolved conflicts land nothing. Inspect the menu and resolve every conflicting path together with `keep_both`, `keep_theirs`, or `keep_ours`; re-preview after primary changes.
4. `drop` only accidental/generated material, never to hide failed checks or unfinished work. Suggested task paths do not filter promotion; confinement remains the authority boundary.
5. {% if profile_has_reject_overlay %}`reject_overlay` is terminal{% else %}Rejection is terminal{% endif %}: use it only for wrong, superseded, duplicate, or unwanted work. Preserve partial delivery{% if profile_has_instruct_worker %} and send its worker back to finish with `instruct_worker`{% endif %}. Do not redispatch already-delivered work.

{% if profile_has_preview_overlay %}Use `preview_overlay` for a closer review; {% endif %}Verify the combined result as its behavior requires. Update progress as results land.
{% include "partials/coordinator-needs-decision-resume.md" %}
