{% if held_call_handle %}A held call settled: handle={{ held_call_handle }} tool={{ held_call_tool }} outcome={{ held_call_outcome }}.

Read it with `held_result(handle="{{ held_call_handle }}")`, then resume. To await another live batch job: `wait(conditions=[{"kind":"process_done","handles":["<handle>"]}])`.{% else %}A command job is terminal.

{% if command_completion %}{{ command_completion }}{% endif %}

This terminal envelope is authoritative; do not rerun. Need more output? Use `command_output(handle="<handle>", cursor=0)` if unread, otherwise the last `next_cursor`. Then resume. For another live batch command, use `wait` with `process_done` (do not await background daemons).{% endif %}
