Folder removed ({{ path }}) — host canceled bound work:
{% for w in workers %}• {% if w.worker %}worker `{{ w.worker }}` ({{ w.agent_type }}){% else %}a {{ w.agent_type }} worker{% endif %}: {{ w.reason }}
{% endfor %}{% for o in overlays %}• changes from {% if o.worker %}worker `{{ o.worker }}`{% else %}a worker{% endif %} rejected
{% endfor %}Re-plan from current roots; do not wait on cancelled workers.
