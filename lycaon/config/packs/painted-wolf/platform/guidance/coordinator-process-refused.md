A running command job met a sandbox refusal. It is still running.

{% if command_refusal %}{{ command_refusal }}{% endif %}

A process waiting on a refused operation may never finish. If the job needs the operation, stop it with `command_stop(handle="<handle>")` and rerun it with the capability the refusal names; if the operation was optional, `wait` on the handle again with `process_done`. Read more output with `command_output(handle="<handle>", cursor=<next_cursor>)`.
