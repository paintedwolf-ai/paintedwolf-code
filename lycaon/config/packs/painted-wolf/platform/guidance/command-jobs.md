## Live handles

Host-authoritative ledger of this session's jobs.
{% for job in jobs %}{% if job.exited %}- handle={{ job.handle }} state=exited exit_code={{ job.exit_code }}{% if job.refusals %} sandbox_refusals={{ job.refusals }}{% endif %} tool={{ job.origin_tool }}
{% else %}- handle={{ job.handle }} mode={{ job.mode }} tool={{ job.origin_tool }} elapsed={{ job.elapsed }}{% if job.deadline_in %} deadline_in={{ job.deadline_in }}{% endif %}
{% endif %}{% if job.command %}  command={{ job.command }}
{% endif %}{% endfor %}
{% if has_running %}{% if profile_has_command_output %}Read `command_output(handle="<handle>", cursor=0)`; continue from `next_cursor` — never reuse.{% else %}Live job output stays recorded on `<handle>`.{% endif %}{% if profile_has_command_stop %} Stop: `command_stop(handle="<handle>")`.{% endif %} Do not relaunch a live job.
{% endif %}{% if has_exited %}An exited row shows a terminal state you have yet to receive in a result. {% if profile_has_command_output %}Read the output and refusals with `command_output` using the last `next_cursor`, or 0 for an unread stream.{% else %}Its output and refusals remain recorded on `<handle>`.{% endif %}
{% endif %}{% if has_held %}mode=held is a call that outlived its wait and keeps running.{% if profile_has_held_result %} Read `held_result(handle="<handle>", wait_ms=30000)`.{% else %} Output remains on `<handle>`.{% endif %}{% if profile_has_held_stop %} Stop: `held_stop(handle="<handle>")`.{% endif %} Do not repeat the call.
{% endif %}{% if (has_running or has_held) and wait_process_done_allowed %}No read needed → `wait(until_complete=true,conditions=[{"kind":"process_done","handles":["<handle>"]}])`.{% endif %}
