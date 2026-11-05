## command{% if is_coordinator %} (coordinator){% endif %}

{% if is_coordinator and command_on_floor == false %}Loaded via `request_tools` for this turn. {% endif %}`cd`, variables and `$!` last one program; job handles span calls.{% if profile_has_terminal_open and profile_has_terminal_send %} For live input, use `terminal_open` and `terminal_send`.{% endif %}

{% if workspace_roots %}Programs start in {{ project_dir }}; `pwd`, `$PWD` and `$OLDPWD` are absolute.{% if root_count > 1 %} `cd` moves between folders by absolute path or `@label`.{% endif %} Unquoted `@label/...` and `@scratch/...` expand in arguments and redirects; quote to keep them literal:
{% for r in workspace_roots %}- `@{{ r.label }}` → {{ r.path }}{% if r.is_primary %} (primary){% endif %}
{% endfor %}Only these project folders have indexing, history and undo.{% if profile_command_mutation != false %} {% if reviews_confined_writes == false %}Other paths ask the person first, except temporary and cache folders.{% else %}Other paths, including temporary folders, ask the person first.{% endif %}{% endif %}
{% endif %}{% if session_scratch_dir %}- `@scratch` → {{ session_scratch_dir }}: session scratch (`$TMPDIR`, `mktemp`); literal `/tmp` is the system's own.
{% endif %}
{% if profile_command_mutation != false %}External programs run in a sandbox unless the person approves host access.{% if not command_external_hidden %} For a native-name collision, use `external -- NAME` or `external -p PATH -- NAME`.{% endif %}

{% if command_output_files %}For long output, read the saved copy named in the result.{% endif %}

`&` starts a session job; `$!`/`jobs` return its handle. Join with `wait %1`/`wait $!`; stop with `kill %1`/`kill $!`.{% if profile_has_command_output %} Across calls, page `command_output` by cursor; follow `next_cursor`.{% endif %}{% if profile_has_command_stop %} Stop with `command_stop`.{% endif %}{% if profile_has_wait %} Across turns, use offered `wait` conditions.{% endif %} `ps`/`pgrep` inspect session processes; `kill` signals them.{% else %}On this read-only surface, `command` runs read-only native utilities and pipelines only; file writes, output redirects to files, and external programs are refused.{% endif %}
