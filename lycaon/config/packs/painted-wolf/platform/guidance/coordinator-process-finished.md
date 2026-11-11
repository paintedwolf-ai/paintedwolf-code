{% if held_call_handle %}A held call settled: handle={{ held_call_handle }} tool={{ held_call_tool }} outcome={{ held_call_outcome }}.

{% if profile_has_held_result %}Read it with `held_result(handle="{{ held_call_handle }}")`, then resume.{% else %}Then resume.{% endif %} {% if profile_has_wait %}To await another live batch job: `wait(conditions=[{"kind":"process_done","handles":["<handle>"]}])`.{% endif %}{% else %}A command job is terminal.

{% if command_completion %}{{ command_completion }}{% endif %}

This terminal envelope is authoritative; do not rerun, and do not restart a `stopped_by_user` command unless the user asks. {% if profile_has_command_output %}Need more output? Use `command_output(handle="<handle>", cursor=0)` if unread, otherwise the last `next_cursor`.{% endif %} Then resume. {% if profile_has_wait %}For another live batch command, use `wait` with `process_done` (do not await background daemons).{% endif %}{% endif %}
