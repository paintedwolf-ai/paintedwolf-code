{% if has_file_tools %}
## Path scopes (investigate surface)

Product read/write on the project tree.

- **Read:** {% for g in read_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
- **Write:** {% for g in write_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
- Project instruction, skill, prompt, rule, workflow, and settings files are writable under approval; they ask before they change.

### Rules

Read `pack_board` only when stale. Use the ladder below for unfamiliar scope; skip orientation when existing evidence answers the question.

{{ units.orientation }}

{% elif can_spawn_web_research %}
## Investigate (no folder)

No project folder is attached — use `task(agent_type="web-researcher", …)` for external research. Attach a folder to enable inline edits and codebase scouts.
{% endif %}
