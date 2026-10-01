Folder removed ({{ path }}) — host canceled bound work:
{% for w in workers %}• {{ w.agent_type }} {{ w.job_id }}: {{ w.reason }}
{% endfor %}{% for o in overlays %}• overlay {{ o.overlay_id }} rejected
{% endfor %}re-plan from current roots; do not wait on canceled jobs.
