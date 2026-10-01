## Compacted continuation record
{% if facts %}
### Facts retained from the transcript
{% for line in facts %}- {{ line }}
{% endfor %}{% endif %}{% if completed %}
### Completed
{% for line in completed %}- {{ line }}
{% endfor %}{% endif %}{% if pending %}
### Pending
{% for line in pending %}- {{ line }}
{% endfor %}{% endif %}
### Reacquire before relying on compacted details
{% if recall_available %}Rows the summary replaced stay on record: `recall(query="handle:<handle>")` returns one as it was observed, and `changed` on the hit means the file moved on since — re-read it before editing. A source with no handle must be re-run or re-read.{% else %}Re-run or re-read each source below before relying on details the summary replaced.{% endif %}
{% for line in reacquire %}- {{ line }}
{% endfor %}{% for s in compacted_sources %}- {% if s.handles %}`{% for h in s.handles %}{{ h }}{% if not forloop.Last %}`, `{% endif %}{% endfor %}`{% if s.tool %} ({{ s.tool }}){% endif %}{% elif s.tool %}{{ s.tool }} result{% if s.message_id %}, message `{{ s.message_id }}`{% endif %} — no handle; re-run it{% else %}message `{{ s.message_id }}` — no handle; re-read it{% endif %}
{% endfor %}{% if constraints %}
### Constraints
{% for line in constraints %}- {{ line }}
{% endfor %}{% endif %}
