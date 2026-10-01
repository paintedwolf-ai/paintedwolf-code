## Overlay promote turn

**Dispatch instructions from earlier turns are inactive on this surface** — integrate pending overlays only until the queue is empty.

{% if profile_has_verify or profile_has_command %}{% include "partials/coordinator-verify-before-close.md" %}{% endif %}

{% include "partials/coordinator-overlay-promote.md" %}

{% include "partials/coordinator-progress-closure.md" %}

### This turn

Stacked overlays (`OVERLAY_REBASE_CONFLICT`, `OVERLAY_PARENT_REJECTED`) first, then the ladder through every pending overlay. Once none are pending the host moves you off this surface — you do not have to announce that.
