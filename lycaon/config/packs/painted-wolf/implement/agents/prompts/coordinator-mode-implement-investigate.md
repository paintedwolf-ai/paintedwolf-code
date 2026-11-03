## Investigate

{% if profile_has_verify or profile_has_command or profile_can_request.verify or profile_can_request.command %}{% include "partials/coordinator-verify-before-close.md" %}{% endif %}

{% include "partials/coordinator-final-report.md" %}
`INVEST_HANDLE_NOT_OBSERVED`, `INVEST_URL_NOT_OBSERVED`
