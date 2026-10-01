{% if execution_mode_entered == "investigate" %}{% include "partials/coordinator-mode-entered-investigate.md" %}
{% elif execution_mode_entered == "orchestrate" %}{% include "partials/coordinator-mode-entered-orchestrate.md" %}
{% elif execution_mode_entered == "wrapup" %}{% include "partials/coordinator-mode-entered-wrapup.md" %}
{% endif %}
{% if execution_mode_left == "investigate" %}{% include "partials/coordinator-mode-left-investigate.md" %}
{% elif execution_mode_left == "orchestrate" %}{% include "partials/coordinator-mode-left-orchestrate.md" %}
{% elif execution_mode_left == "wrapup" %}{% include "partials/coordinator-mode-left-wrapup.md" %}
{% endif %}
