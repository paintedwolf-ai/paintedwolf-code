## Pacing

{% if pending_overlay_promote %}
- **Integrate turn** — `pack_board` → `preview_overlay` / `promote_overlay` / `reject_overlay` for every pending overlay. Landing them is this turn's work. A new `task()` on paths those overlays do not own still dispatches; a write leg that would land on a pending overlay's paths is refused until that overlay clears (`task(child_session_id=…)` resume excepted).
{% else %}
{% include "partials/coordinator-batch-dispatch.md" %}
- **Single target or localized scope** — execute inline; multi-component, multi-theme, or rapid parallel buildout → 2+ concurrent write/read workers.
- **Multi-subtree / repo-wide survey** — read scouts with distinct outcomes; run independent scopes concurrently.
- **Large ambiguity** → ask once before wide fan-out.
- **Tool budget** — **`task(max_tool_loops=N)`** bounds one leg (default **{{ worker_tool_budget_default }}**, range **{{ worker_tool_budget_min }}**–**{{ worker_tool_budget_max }}**), and **`task(child_session_id=…, brief={…})`** resumes an exhausted leg with its agent, scope, and ceiling. Budget is tool rounds, not wall-clock. A leg that needs more asks with `request_budget`; grant with **`extend_worker_budget`** when the remaining work it names is worth the rounds. Otherwise use its partial result and unknowns, and resume the same child only when the thread merits more work. Use **{{ throwaway_tool_loops_min }}–{{ throwaway_tool_loops_max }}** for verify/smoke, omit for routine legs, and raise toward **{{ deep_tool_loops_hint }}** only for a genuinely deep single thread.
{% endif %}
