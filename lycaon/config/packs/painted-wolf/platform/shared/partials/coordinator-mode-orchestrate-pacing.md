## Pacing

{% include "partials/coordinator-batch-dispatch.md" %}
- **Single target or localized scope** — execute inline; multi-component, multi-theme, or rapid parallel buildout → 2+ concurrent write/read workers.
- **Multi-subtree / repo-wide survey** — read scouts with distinct outcomes; run independent scopes concurrently.
- **Large ambiguity** → ask once before wide fan-out.
- **Resuming unfinished legs** — {% if profile_has_task %}**`task(child_session_id=…, brief={…})`** resumes a leg that ended without finishing, with its agent and scope.{% else %}A leg that ended without finishing resumes with its agent and scope.{% endif %}
