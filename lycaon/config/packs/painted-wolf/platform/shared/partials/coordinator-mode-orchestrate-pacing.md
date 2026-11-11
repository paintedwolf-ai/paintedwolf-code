## Pacing

{% include "partials/coordinator-batch-dispatch.md" %}
- **Single target or localized scope** — execute inline; multi-component, multi-theme, or rapid parallel buildout → 2+ concurrent write/read workers.
- **Multi-subtree / repo-wide survey** — read scouts with distinct outcomes; run independent scopes concurrently.
- **Large ambiguity** → ask once before wide fan-out.
- **Resuming exhausted legs** — {% if profile_has_task %}**`task(child_session_id=…, brief={…})`** resumes an exhausted leg with its agent, scope, and ceiling.{% else %}An exhausted leg resumes with its agent, scope, and ceiling.{% endif %}{% if profile_has_extend_worker_budget %} Grant additional tool rounds with **`extend_worker_budget`** when the remaining work is worth the rounds.{% endif %}
