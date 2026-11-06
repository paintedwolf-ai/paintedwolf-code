## Overlay promote turn

Review pending overlays against the requested outcome.

{% if profile_has_verify or profile_has_command or profile_can_request.verify or profile_can_request.command %}{% include "partials/coordinator-verify-before-close.md" %}{% endif %}

{% include "partials/coordinator-overlay-promote.md" %}


### This turn

Stacked overlays (`OVERLAY_REBASE_CONFLICT`, `OVERLAY_PARENT_REJECTED`) first, then the ladder through every pending overlay.
