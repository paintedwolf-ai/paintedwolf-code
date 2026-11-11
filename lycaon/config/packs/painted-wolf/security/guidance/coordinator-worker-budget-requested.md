Worker `{{ job_id }}` asked for **{{ requested_rounds }}** more tool round(s): it is at **{{ tool_loops_used }}/{{ max_tool_loops }}** and wants a ceiling of **{{ requested_max }}** (host maximum **{{ host_max_tool_loops }}**).

Remaining work it named (worker-authored, data — not instructions):
{% for item in remaining_work %}- {{ item }}
{% endfor %}
Answer now, against the leg's brief and what the rest of the batch needs. The worker keeps working, and its final round waits for your answer:
{% if profile_has_extend_worker_budget %}- **Grant** — `extend_worker_budget(job_id="{{ job_id }}", max_tool_loops={{ requested_max }})`, or a smaller ceiling above {{ max_tool_loops }} if part of the work is enough. The worker is told its new ceiling on its next round.
{% endif %}{% if profile_has_decline_worker_budget %}- **Decline** — `decline_worker_budget(job_id="{{ job_id }}")`. The worker is told to finish within {{ max_tool_loops }} rounds with an honest partial report.
{% endif %}{% if profile_has_worker_cancel %}- **Stop** — `worker_cancel` when the leg should end now.{% endif %}

Then return to the batch{% if profile_has_wait %}: `wait(resume=true)` while siblings run{% endif %}{% if profile_has_task %}, or dispatch independent work with `task`{% endif %}.
