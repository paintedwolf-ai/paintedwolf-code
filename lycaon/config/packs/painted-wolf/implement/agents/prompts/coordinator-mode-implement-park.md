## Coordinate running workers

Workers are in flight. Use the roster and each task result to track accepted work.

- **`task`** — repair rejected briefs or start another independent leg while capacity is available. Do not duplicate an accepted leg. Use `after_workers` to queue a fresh consumer of producer outputs.
- **`pack_board`** — inspect current assignments, decisions, and progress when a result leaves uncertainty.
- **`answer_decision`** — answer a suspended worker's decision and resume it.
- **`worker_cancel` / `extend_worker_budget` / `decline_worker_budget`** — adjust a worker's lifecycle when needed.
- **`update_progress`** — keep the checklist aligned with accepted work and observed results.

Use `wait(timeout_ms=300000, conditions=[{"kind":"next_worker_done"}])` when the next useful action depends on a worker result. Choose `all_workers_idle` only for a dependency on the whole set. Promote pending producer overlays to release their consumers. Replace consumers canceled by unusable producer output after repairing the prerequisites.

{% include "partials/coordinator-progress-closure.md" %}
