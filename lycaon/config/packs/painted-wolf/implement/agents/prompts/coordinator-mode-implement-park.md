## Coordinate running workers

Workers are in flight. Use the roster and each task result to track accepted work.
{% if profile_has_task %}
- **`task`** — repair rejected briefs or start another independent leg while capacity is available. Do not duplicate an accepted leg. Use `after_workers` to have a new worker wait on its producers.{% endif %}{% if profile_has_instruct_worker %}
- **`instruct_worker`** — send a worker more to do; one between rounds starts its next round in its own workspace.{% endif %}{% if profile_has_pack_board %}
- **`pack_board`** — inspect current assignments, decisions, and progress when a result leaves uncertainty.{% endif %}{% if profile_has_answer_decision %}
- **`answer_decision`** — answer an asking worker's question so it continues.{% endif %}{% if profile_has_worker_cancel %}
- **`worker_cancel`** — stop a worker whose round is no longer worth continuing. Round notices tell you when a round runs long or goes quiet.{% endif %}{% if profile_has_update_progress %}
- **`update_progress`** — keep the checklist aligned with accepted work and observed results.{% endif %}
{% if profile_has_wait %}
Use `wait(timeout_ms=300000, conditions=[{"kind":"next_worker_done"}])` when the next useful action depends on a worker result. Choose `all_workers_idle` only for a dependency on the whole set.{% endif %} Land a producer's changes to release the workers waiting on it. A worker whose producer was closed or rejected keeps waiting and raises a notice; start a replacement or withdraw it.
