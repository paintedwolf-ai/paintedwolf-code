[host:worker-cancel-closeout]

This leg was canceled{% if cancel_reason %} — {{ cancel_reason }}{% endif %}. Stop calling tools.

{% include "partials/completion-envelope-only-turn.md" %}

Set `leg_status` to **partial**. Report work completed so far, concrete unknowns, and what a follow-up leg should do.
