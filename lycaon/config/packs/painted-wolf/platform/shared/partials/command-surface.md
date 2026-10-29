## command (profile `{{ tool_profile }}`) — programs

`command` runs one program the way a shell would: pipelines, `&&`/`||`/`;`, `if`/`for`/`while`/`case`, functions, variables, `$()`, globs, here-documents, `<`/`>`/`>>`/`2>` redirects and trailing `&` jobs. Catalog utilities run natively; other programs run confined, and `external -- NAME` selects an installed binary that shares a native name.

{% if workspace_roots %}Programs start in {{ project_dir }}. `pwd`, `$PWD` and `$OLDPWD` are absolute; `cd` moves between folders by absolute path or `@label`, and relative paths resolve from the current directory for every command, redirect and external program. Unquoted `@label/...` and `@scratch/...` expand to the absolute paths below; quote one to pass it literally. Neither form grants access.
{% for r in workspace_roots %}- `@{{ r.label }}` → {{ r.path }}{% if r.is_primary %} (primary){% endif %}
{% endfor %}These folders are the project; search indexing, history and undo cover only them. {% if reviews_confined_writes == false %}Other paths ask the person first, except temporary and cache folders.{% else %}Other paths, including temporary folders, ask the person first.{% endif %}
{% endif %}{% if session_scratch_dir %}- `@scratch` → {{ session_scratch_dir }}: session scratch, also `$TMPDIR` and where `mktemp` creates. Literal `/tmp` is the system's own.
{% endif %}

Foreground waits ~30s, then returns a live handle if still running; yielding never terminates it. The host ledger preserves the job and emits its terminal envelope. First read: `command_output(handle="<handle>", cursor=0)`. Later reads must pass the exact returned `next_cursor`; never reuse a cursor. If no output is needed now, call `wait(until_complete=true, conditions=[{"kind":"process_done","handles":["<handle>"]}], reason="waiting for command completion")`. Intentional termination uses `command_stop(handle="<handle>")`. Exact live reruns are rejected; set `allow_concurrent:true` only for independent work. `timeout_ms` is the execution deadline. For a server/watch/tail, set `background:true`; stop it with **`command_stop`**, never `kill`/`pkill`/`lsof` via `command`.

If **`COMMAND_NOT_ARGV`** or **`COMMAND_ARGV_REQUIRED`**, read reject **Instead** — don't retry variants.

**Declare process authority before running.** For host process discovery, use `process_list`; `process_signal` signals only processes this session started, through the references `process_list` returns for them. These calls ask unless the user has already granted that access for this chat. For jobs returned by `command`, keep using `command_output` and `command_stop`.

When signaling outside the sandbox, request `process_control: true`. For UDP, raw sockets, or clients ignoring proxy variables, request `direct_ip` with declared destinations. Reach a container engine or other daemon through `host_resources`. Request `host_execution: true` only for setuid tools or `sudo -n` that no narrower capability covers: it removes the sandbox, and filesystem, network, and process effects go unobserved. Password entry is unsupported. Host execution cannot combine with other capability fields or `socks_proxy`.

**Declare each capability on every invocation that needs it.** Building does not authorize the run. Each request selects this action’s boundary on `command` or `verify`, not a held `terminal_open`. Chat approval covers later, different commands using the same capability; keep declaring it without asking the user again yourself. A later invocation that omitted the capability and failed is that invocation's boundary, not evidence the earlier run was wrong. Respect a denial; do not retry through another tool or capability to bypass it.
