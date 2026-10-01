## Live handles

Host-authoritative ledger: do not relaunch jobs.
{% for job in jobs %}- handle={{ job.handle }} mode={{ job.mode }} tool={{ job.origin_tool }} elapsed={{ job.elapsed }}{% if job.deadline_in %} deadline_in={{ job.deadline_in }}{% endif %}
{% if job.command %}  command={{ job.command }}
{% endif %}{% endfor %}
{% if has_command %}Read `command_output(handle="<handle>", cursor=0)`; continue from `next_cursor` — never reuse. Stop: `command_stop(handle="<handle>")`. `allow_concurrent:true` is only for independent jobs; live reruns reject.
{% endif %}{% if has_held %}mode=held is a call that outlived its wait and keeps running. Read `held_result(handle="<handle>", wait_ms=30000)`; stop: `held_stop(handle="<handle>")`. Do not repeat the call.
{% endif %}{% if can_wait %}No read needed → `wait(until_complete=true,conditions=[{"kind":"process_done","handles":["<handle>"]}])`.{% endif %}
