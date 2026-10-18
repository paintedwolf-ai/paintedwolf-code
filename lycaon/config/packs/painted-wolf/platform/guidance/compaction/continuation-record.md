{% if level == "references" %}[host:compacted history through ordinal {{ through_ord }}]
{% if recall_available %}Recover recorded details with `recall(history={"after_ord":0,"through_ord":{{ through_ord }}})`. Use `search` for an index, `message_id` for a body, and `format="record"` for original tool arguments. Pass returned `next_request` to continue.{% else %}Reacquire omitted observations before relying on them.{% endif %}
{% for s in compacted_sources %}{% if s.handles %}{% for h in s.handles %}`{{ h }}`{% if recall_available %}: `recall(query="handle:{{ h }}")`{% endif %}
{% endfor %}{% elif s.message_id %}{% if recall_available and through_ord %}{{ s.tool }}: `recall(history={"through_ord":{{ through_ord }},"message_id":"{{ s.message_id }}","format":"record"})`
{% else %}{{ s.tool }} message_id `{{ s.message_id }}`
{% endif %}{% endif %}{% endfor %}
{% else %}## Compacted continuation record
{% if summary_through_ord %}Latest summarized observation: ordinal {{ summary_through_ord }}. This is a selective summary.{% if summary_through_ord < through_ord %} Newer covered observations remain references.{% endif %}
{% endif %}
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
{% if not recall_available %}Reacquire the needed evidence through available read tools.{% endif %}
{% for line in reacquire %}- {{ line }}
{% endfor %}{% for s in compacted_sources %}- {% if s.handles %}`{% for h in s.handles %}{{ h }}{% if not forloop.Last %}`, `{% endif %}{% endfor %}`{% if s.tool %} ({{ s.tool }}){% endif %}{% if recall_available %}{% for h in s.handles %} · `recall(query="handle:{{ h }}")`{% endfor %}{% endif %}{% elif recall_available and through_ord and s.message_id %}{{ s.tool }} · `recall(history={"through_ord":{{ through_ord }},"message_id":"{{ s.message_id }}","format":"record"})`{% else %}{% if s.tool %}{{ s.tool }}, {% endif %}message `{{ s.message_id }}`{% endif %}
{% endfor %}{% if constraints %}
### Constraints
{% for line in constraints %}- {{ line }}
{% endfor %}{% endif %}

{% if through_ord %}### Covered history
Canonical observations through ordinal {{ through_ord }} remain on record. {% if recall_available %}Use `recall(history={"after_ord":0,"through_ord":{{ through_ord }}})` to page their index. Supply a returned `message_id` to recover its body; `format="record"` includes original parts and tool arguments; `offset` and `next_offset` page long bodies. {% endif %}These are recorded observations, not current file contents or verification. Re-read changed sources before editing.
{% endif %}{% if reference_only %}Some covered observations have no accepted prose summary. Recover the relevant records before relying on their details.
{% endif %}
{% endif %}
