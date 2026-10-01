## Investigate

Live jobs: `command_output` by cursor; `wait`; `command_stop` only to terminate. Held call (`held-N`): `held_result`; `held_stop` only to cancel.

{% if profile_has_verify or profile_has_command %}{% include "partials/coordinator-verify-before-close.md" %}{% endif %}

{% include "partials/coordinator-final-report.md" %}
`INVEST_HANDLE_NOT_OBSERVED`, `INVEST_URL_NOT_OBSERVED`
