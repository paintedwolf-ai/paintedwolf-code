## Pacing

{% include "partials/coordinator-batch-dispatch.md" %}
- **Single target or localized scope** — execute inline; multi-component, multi-theme, or rapid parallel buildout → 2+ concurrent write/read workers.
- **Multi-subtree / repo-wide survey** — read scouts with distinct outcomes; run independent scopes concurrently.
- **Large ambiguity** → ask once before wide fan-out.
{% if profile_has_instruct_worker %}- **Unfinished or related work** — **`instruct_worker`** sends a worker back with what remains or with related follow-up, in the same conversation and with what it already learned; corrections need no restated brief. Unrelated work goes to a new worker.
{% endif %}
