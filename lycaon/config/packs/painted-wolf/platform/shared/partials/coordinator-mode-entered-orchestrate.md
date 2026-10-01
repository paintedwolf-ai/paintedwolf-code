## Entered orchestrate

Coordinate the remaining work with the tools offered on this request. {% if profile_has_task %}Use `task` to dispatch another worker for independent work.{% endif %}{% if profile_has_wait %} Use `wait(timeout_ms=300000, conditions=[{"kind":"next_worker_done"}])` when the next useful action depends on a worker result.{% endif %} Review returned results and resolve worker decisions. {% if not profile_has_command %}Inline commands are intentionally unavailable. {% endif %}Waiting for time to pass does not make an unavailable tool callable.
{% include "partials/execution-mode-supersession.md" %}
