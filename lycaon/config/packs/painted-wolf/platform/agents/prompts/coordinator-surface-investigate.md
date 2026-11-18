{% if has_file_tools %}
## Path scopes (investigate surface)

Product read/write on the project tree.

- **Read:** {% for g in read_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
- **Write:** {% for g in write_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
- Project instruction, skill, prompt, rule, workflow, and settings files are writable under approval; they ask before they change.

### Rules

{% if profile_has_pack_board %}Read `pack_board` only when stale. {% endif %}Use the ladder below for unfamiliar scope; skip orientation when existing evidence answers the question.

{{ units.orientation }}

{% elif can_spawn_web_research %}
## Investigate (no folder)

No project folder is attached{% if profile_has_task %} — use `task(agent_type="web-researcher", …)` for external research{% elif profile_has_request_tools %} — load worker dispatch through `request_tools`, then start a `web-researcher` for external research{% endif %}. Attach a folder to enable inline edits and codebase scouts.
{% endif %}
