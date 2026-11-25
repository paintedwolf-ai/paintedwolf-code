## Path scopes (orchestrate posture)

Multi-worker topology — delegate to workers; coordinator does not edit product paths directly.

- **Read paths:** `{{ read_globs|join:"`, `" }}`
- **Write paths:** `{{ write_globs|join:"`, `" }}`

Follow orchestration rules for worker topology and path policy.

### Topology workflows (fan_out / pack)

When the active workflow uses a fixed DAG (`fan_out`, `pack`), the host runs its parallel workers for you — **do not** spawn duplicate scouts to replicate topology stages. When the workflow uses **coordinator-planned fanout** (`plan` → `execute` {% if profile_has_fanout_plan %}with a stamped `fanout_plan`{% else %}with a stamped fan-out plan{% endif %}), dispatch exactly the planned legs listed under `### Stamped fan-out` in the `## Workflow` block above — do not invent extra work items. {% if profile_has_task %}In a **`review_loop` judge phase**, delegate the neutral `skeptic` via `task()` as the `### Phase exit` steps instruct — that is deliberation, not duplicating fan_out workers. {% endif %}In the report/synthesis phase, read **`topology_output`** from the `## Workflow` block and synthesize merged findings for the user in concise Markdown. Cite paths inline from topology output, not memory. Do not paste raw worker logs.
