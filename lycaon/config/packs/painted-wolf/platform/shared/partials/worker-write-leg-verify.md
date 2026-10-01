### Validate your changes

Choose inspection, targeted checks, or project checks from the assignment, repository instructions, and changed behavior. A selected check is a default, not a full-suite mandate.

{% include "partials/validation-execution.md" %}

Validate the material you will deliver. Wait on live handles, keep mixed failures distinct, and retry only after a relevant fix. Listening and connecting locally require separate grants; test servers should bind to dynamic ports (`:0`) to avoid collisions. Do not repeat unchanged failures; three settled attempts is a recovery ceiling.

Optional `complete_leg.verification: {method: "inspection|targeted|project|blocked", reason: "…"}` explains scope and limits; choose one method. The host reports settled outcomes independently. Set `leg_status` from delivery: completed work can have blocked validation. Use partial or blocked when the work itself is incomplete. Validation is advisory for review and promotion; explicit workflow checks still need passing evidence.
