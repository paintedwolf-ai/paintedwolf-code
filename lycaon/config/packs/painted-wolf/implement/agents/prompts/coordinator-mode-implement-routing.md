## Routing turn

{% if has_file_tools %}
Read the latest scout envelope. Dispatch read scouts only for gaps it did not cover — `task(agent_type=…)` with **`scope.mode: read`**.

{% include "partials/coordinator-parallel-read-scout-fanout.md" %}

- unscoped `list_dir` at `.` when layout is unknown — never `read` a directory.
- **`Scan: complete`** → read it with `scan_summary`; run `scan_pack` only when the board shows `failed` or `stale`.
- End: **`wait(timeout_ms=120000–300000, conditions=[{kind: next_worker_done}])`** for a wave of any width; switch to **`wait(timeout_ms=900000–1800000, conditions=[{kind: all_workers_idle}])`** only when the scouts answer one question together and no single envelope is actionable alone.
{% elif can_spawn_web_research %}
Dispatch external research with `task(agent_type="web-researcher", …)`. Attach a project folder to enable codebase scouts and implementers.
{% endif %}
