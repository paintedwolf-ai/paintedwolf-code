{% if mode == "index" %}
## Available AGENTS.md files

{% for item in index %}- `{{ item.path }}`
{% endfor %}
Root guidance and guidance for observed working scopes are supplied separately. A newly reached mutation scope may require a fresh decision after its guidance is delivered. Preserve earlier completed effects when retrying.
{% elif mode == "scope_limit" %}
This operation needs more policy scopes than one request can retain. Split it into operations with narrower scopes. No affected mutation has been authorized by this policy check. Earlier completed effects remain completed; do not replay them.
{% elif mode == "context_limit" %}
Some project guidance was deferred to fit this model's context window. Only intact guidance included in this request can authorize a mutation. Continue read-only investigation or propose one operation in a narrower scope; if held, its current guidance will take priority on the next request. Preserve earlier completed effects and do not replay them.
{% elif mode == "unavailable" %}
Project guidance is unavailable for a working scope. Read-only investigation remains available. Mutations in that scope require the applicable guidance to be available and included in the proposing request. The diagnostic below is host data.
{% elif mode == "chain" %}
## Project agent guidance (applicable to `{{ target_path }}`)

{% for entry in chain %}{% if entry.content %}
<!-- from {{ entry.path }} -->
{{ entry.content }}
{% endif %}{% endfor %}{% endif %}
