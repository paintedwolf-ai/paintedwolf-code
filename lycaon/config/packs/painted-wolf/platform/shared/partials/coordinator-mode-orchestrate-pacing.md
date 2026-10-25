## Pacing

{% include "partials/coordinator-batch-dispatch.md" %}
- **Single target or localized scope** — execute inline; multi-component, multi-theme, or rapid parallel buildout → 2+ concurrent write/read workers.
- **Multi-subtree / repo-wide survey** — read scouts with distinct outcomes; run independent scopes concurrently.
- **Large ambiguity** → ask once before wide fan-out.
- **Resuming exhausted legs** — **`task(child_session_id=…, brief={…})`** resumes an exhausted leg with its agent, scope, and ceiling. Grant additional tool rounds with **`extend_worker_budget`** when the remaining work is worth the rounds.
