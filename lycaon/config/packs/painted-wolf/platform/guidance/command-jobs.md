## Live handles

Host-authoritative ledger of this session's jobs.
{% for job in jobs %}{% if job.exited %}- handle={{ job.handle }} state=exited exit_code={{ job.exit_code }}{% if job.refusals %} sandbox_refusals={{ job.refusals }}{% endif %} tool={{ job.origin_tool }}
{% else %}- handle={{ job.handle }} mode={{ job.mode }} tool={{ job.origin_tool }} elapsed={{ job.elapsed }}{% if job.deadline_in %} deadline_in={{ job.deadline_in }}{% endif %}
{% endif %}{% if job.command %}  command={{ job.command }}
{% endif %}{% endfor %}
{% if has_running %}Read `command_output(handle="<handle>", cursor=0)`; continue from `next_cursor` — never reuse. Stop: `command_stop(handle="<handle>")`. Do not relaunch a live job.
{% endif %}{% if has_exited %}An exited row shows a terminal state you have yet to receive in a result. Read the output and refusals with `command_output` using the last `next_cursor`, or 0 for an unread stream.
{% endif %}{% if has_held %}mode=held is a call that outlived its wait and keeps running. Read `held_result(handle="<handle>", wait_ms=30000)`; stop: `held_stop(handle="<handle>")`. Do not repeat the call.
{% endif %}{% if can_wait %}No read needed → `wait(until_complete=true,conditions=[{"kind":"process_done","handles":["<handle>"]}])`.{% endif %}
