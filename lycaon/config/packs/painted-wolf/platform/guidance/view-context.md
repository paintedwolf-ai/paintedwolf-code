## In view

{% if editors or diffs or folder or search_query or findings %}What the person had in view when they sent the next message, most recently focused window first:
{% for e in editors %}- Editor: {{ e.path }}{% if e.unsaved %} (unsaved){% endif %}{% if e.selected %}{% if e.cursor %}, cursor on line {{ e.start_line }}{% elif e.start_line == e.end_line %}, line {{ e.start_line }} selected{% else %}, lines {{ e.start_line }}-{{ e.end_line }} selected{% endif %}{% endif %}
{% endfor %}{% for d in diffs %}- Diff: {{ d }}
{% endfor %}{% if folder %}- File tree, folder last opened: {{ folder }}
{% endif %}{% if search_query %}- Search for {{ search_query }}{% if search_hit %}, result {{ search_hit }} selected{% endif %}
{% endif %}{% for f in findings %}- Security finding {{ f.rule_id }} ({{ f.level }}){% if f.location %} at {{ f.location }}{% endif %}
{% endfor %}{% if read_offered and editors %}`read` returns an open file as the editor holds it, unsaved edits included.
{% endif %}{% else %}The person had nothing in view when they sent the next message.
{% endif %}
