## Overlay promote turn

Review pending overlays against the requested outcome. Resume the same child for unfinished work; promote reviewed, wanted changes or reject unwanted changes.

{% if profile_has_verify or profile_has_command %}{% include "partials/coordinator-verify-before-close.md" %}{% endif %}

{% include "partials/coordinator-overlay-promote.md" %}


### This turn

Stacked overlays (`OVERLAY_REBASE_CONFLICT`, `OVERLAY_PARENT_REJECTED`) first, then the ladder through every pending overlay. Once none are pending the host moves you off this surface — you do not have to announce that.
