## Overlay promote turn

Review delivered worker results against the requested outcome{% if profile_has_preview_overlay %}: preview{% endif %}{% if profile_has_promote_overlay %}, promote{% endif %}{% if profile_has_reject_overlay %}, reject{% endif %}{% if profile_has_shelve_overlay %}, shelve{% endif %}{% if profile_has_instruct_worker %}, instruct the worker for follow-up{% endif %}{% if profile_has_task %}, or start a reviewer with `task` and `review_of`{% endif %}.

{% if profile_has_verify or profile_has_command or profile_can_request.verify or profile_can_request.command %}{% include "partials/coordinator-verify-before-close.md" %}{% endif %}

{% include "partials/coordinator-overlay-promote.md" %}


### This turn

Stacked results (`OVERLAY_REBASE_CONFLICT`, `OVERLAY_PARENT_REJECTED`) first, then resolve each unresolved result handle in order.
