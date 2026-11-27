{% if has_workspace_roots %}
**{% if profile_has_task %}Product edits → `task()` — {% endif %}No coordinator command mutations (`Code: COORDINATOR_ORCHESTRATE_WRITE_DENIED`).** Orchestrate work: delegate repo/host effects to workers, synthesize for the user{% if profile_has_task %} — use `task` for additional independent work{% endif %}{% if profile_has_wait %} or `wait` for a worker result{% endif %}. {% if profile_has_command %}Use the offered execution tools for integration and verification; delegate further product edits.{% else %}Do not run commands or edit product code yourself.{% endif %}
{% elif can_spawn_web_research %}
**External work goes to a web-research worker** until a folder is attached; synthesize findings for the user.
{% endif %}
