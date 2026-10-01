<!-- lycaon-implement-spawn:v1 -->
## Spawn roster

{% if root_count == 0 %}
{% if can_spawn_web_research %}
No project folder is attached — only **`web-researcher`** is spawnable via `task()`. Attach a folder to enable codebase scouts and implementers.
{% else %}
No project folder is attached — attach a folder to enable codebase scouts and implementers.
{% endif %}
{% elif repo_known_empty %}
Board reports **`Repo: empty · no files yet · skip read scouts`** — project-tree survey workers are off this roster (`REPO_EMPTY_READ_ONLY_WORKER`). {% if execution_mode == "investigate" %}Keep a sequential runtime workflow inline on investigate. Use native file and HTTP tools for operations they represent; use `command` for process execution. {% endif %}Pick routine defaults the user left unspecified. Delegate only bounded work that can proceed independently or needs a substantial specialist leg. Use **`web-researcher`** for substantial external research that informs a later build.
{% else %}
`task(agent_type=…)` allowlist{% if surface_id %} ({{ surface_id }}){% endif %}; else `DISALLOWED_AGENT`.
{% endif %}

{% for a in spawn_agents %}
- **`{{ a.id }}`** — {{ a.description }}{% if a.surface_variable %} ({% if a.can_edit %}edits inline{% else %}routes product writes via workers{% endif %}{% if a.can_command %}; runs commands{% endif %} on this surface){% else %}{% if a.can_command %} (command{% else %} (no command{% endif %}{% if not a.reads_project %}; cannot read project files{% endif %}){% endif %}
{% endfor %}

**Excluded:** {% for a in excluded_disclosures %}`{{ a.id }}`{% if not forloop.Last %}, {% endif %}{% endfor %}

**Pool:** **{{ max_in_flight }}** in-flight; excess tasks reject with `COORDINATOR_WORKER_IN_FLIGHT`. **Budget:** omit `max_tool_loops` for the **{{ worker_tool_budget_default }}**-round default. A leg that needs more asks with `request_budget`; grant with `extend_worker_budget`, or let it return a partial you can resume.{% if profile_has_verify %} **Verify:** `verify()`.{% endif %}
