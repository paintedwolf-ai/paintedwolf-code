## command{% if is_coordinator %} (coordinator){% endif %}

{% if is_coordinator and command_on_floor == false %}Loaded via `request_tools` for this turn. {% endif %}Utilities run natively with GNU and BSD flags; use `CMD --help`. Shell state lasts for this program only.

{% if workspace_roots %}Programs start in {{ project_dir }}; `pwd`, `$PWD` and `$OLDPWD` are absolute.{% if root_count > 1 %} `cd` moves between folders by absolute path or `@label`.{% endif %} Unquoted `@label/...` and `@scratch/...` expand in arguments and redirects (quote to pass literally):
{% for r in workspace_roots %}- `@{{ r.label }}` → {{ r.path }}{% if r.is_primary %} (primary){% endif %}
{% endfor %}These folders are the project; search indexing, history and undo cover only them.{% if profile_command_mutation != false %} {% if reviews_confined_writes == false %}Other paths ask the person first, except temporary and cache folders.{% else %}Other paths, including temporary folders, ask the person first.{% endif %}{% endif %}
{% endif %}{% if session_scratch_dir %}- `@scratch` → {{ session_scratch_dir }}: session scratch (`$TMPDIR`, `mktemp`); literal `/tmp` is the system's own.
{% endif %}
{% if profile_command_mutation != false %}Other programs (`go`, `python3`, `node`, `npm`, `make`, `cargo`) run inside a sandbox unless the person approves host access.{% if not command_external_hidden %} Use `external -- NAME` (or `external -p PATH -- NAME`) when an installed binary shares a native name.{% endif %}

{% if command_output_files %}For long output, read the saved copy named in the result.{% endif %}

`&` starts a session job. `$!` and `jobs` return its handle; `wait %1` / `wait $!` waits, and `kill %1` / `kill $!` stops it inside `command`.{% if profile_has_command_output %} Across calls, page output with `command_output(handle="<handle>", cursor=0)`, following each returned `next_cursor`.{% endif %}{% if profile_has_command_stop %} Stop a prior call's job with `command_stop(handle="<handle>")`.{% endif %}{% if profile_has_wait %} To wait across turns, use offered `wait` conditions.{% endif %}{% if profile_has_process_list != false or profile_has_process_signal != false %}

For processes this session started, use {% if profile_has_process_list != false %}`process_list` to inspect them{% endif %}{% if profile_has_process_list != false and profile_has_process_signal != false %} and {% endif %}{% if profile_has_process_signal != false %}`process_signal` to signal them{% endif %}.{% endif %}{% else %}On this read-only surface, `command` runs read-only native utilities and pipelines only; file writes, output redirects to files, and external programs are refused.{% endif %}
