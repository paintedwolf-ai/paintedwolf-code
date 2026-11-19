## command{% if is_coordinator %} (coordinator){% endif %}

{% if is_coordinator and command_on_floor == false %}Loaded via `request_tools` for this turn. {% endif %}`cd`, variables and `$!` last one program; job handles span calls.{% if profile_has_terminal_open and profile_has_terminal_send %} For live input, use `terminal_open` and `terminal_send`.{% elif profile_has_terminal_open %} For live input, open with `terminal_open`.{% endif %}

{% if workspace_roots %}Programs start in the session folder; `pwd`, `$PWD` and `$OLDPWD` are absolute. `cd` moves between folders by `@label`. In arguments and redirects, unquoted folder references expand to absolute paths (`@label/...`{% if session_scratch_dir %}, `@scratch/...`{% endif %}).
Only attached folders have indexing, history and undo.{% if profile_command_mutation != false %} {% if reviews_confined_writes == false %}Confined programs can write standard caches and package stores. Other outside writes use the configured approval policy.{% else %}Declared writes outside these folders and scratch are reviewed.{% endif %}{% endif %}
{% endif %}{% if session_scratch_dir %}- `@scratch`: session scratch (`$TMPDIR`, `mktemp`).
{% endif %}{% if profile_command_mutation != false %}Literal `/tmp` names shared machine storage.{% if shared_temp_writable %} Shared temp grants cover {% for location in shared_temp_locations %}`{{ location }}`{% if not forloop.Last %}, {% endif %}{% endfor %}.{% else %} Shared writes outside attached folders and grants require approval.{% endif %}
{% endif %}
{% if profile_command_mutation != false %}{% if profile_command_external_exec != false %}External programs run in a sandbox unless the person approves host access.{% if not command_external_hidden %} For a native-name collision, use `external -- NAME` or `external -p PATH -- NAME`.{% endif %}{% else %}Only native programs run on this surface; external programs are refused.{% endif %}

{% if command_output_files %}For long output, read the saved copy named in the result.{% endif %}

One `command` call can chain orientation ({% if profile_has_survey_repo %}`repo-map`, {% endif %}`outline --path`,{% if profile_has_summarize %} `summarize`,{% endif %} `rg -n`, `find`, `git status`, `git diff`), bounded slices (`sed -n 'A,Bp'`, `cat -n`), and multi-edit sequences (`edit`, `replace-lines`,{% if profile_has_code_rewrite %} `code-rewrite`,{% endif %} `jq-edit`, heredocs).

`&` starts a session job; `$!`/`jobs` return its handle. Join with `wait %1`/`wait $!`; stop with `kill %1`/`kill $!`.{% if profile_has_command_output %} Across calls, page `command_output` by cursor; follow `next_cursor`.{% endif %}{% if profile_has_command_stop %} Stop with `command_stop`.{% endif %}{% if profile_has_wait %} Across turns, use offered `wait` conditions.{% endif %} `ps`/`pgrep` inspect session processes; `kill` signals them.{% else %}On this read-only surface, `command` runs read-only native utilities and pipelines only; file writes, output redirects to files, and external programs are refused.{% endif %}
