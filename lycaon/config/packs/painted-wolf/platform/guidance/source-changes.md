## Source changes since your last turn

Other actors' changes through turn start. Fixed window; UTC.
{% for g in git %}- git {{ g.kind }}{% if g.root %} in {{ g.root }}{% endif %}{% if g.from_ref and g.from_ref != g.to_ref %} {{ g.from_ref }} → {{ g.to_ref }}{% elif g.to_ref %} on {{ g.to_ref }}{% endif %}{% if g.to_commit %} ({% if g.from_commit %}{{ g.from_commit }} → {% endif %}{{ g.to_commit }}){% endif %}{% if g.detail %} — {{ g.detail }}{% endif %} at {{ g.at }}
{% endfor %}{% for f in files %}- {{ f.path }} — {{ f.actor }}{% if f.detail %} ({{ f.detail }}){% endif %} {{ f.op }}{% if f.effects > 1 %} ({{ f.effects }} changes, newest{% endif %} at {{ f.at }}{% if f.effects > 1 %}){% endif %}
{% endfor %}{% if other_files > 0 %}Elsewhere in the project: {{ other_effects }} change(s) across {{ other_files }} file(s) you have not read or written this session.
{% endif %}{% if truncated %}The window overflowed the recording cap, so these counts are a floor. Query `source_history` for the full record.
{% endif %}{% if git %}A git line is the recorded cause of the file changes around it — a checkout or pull moves files without anyone editing them.
{% endif %}{% if files %}Before editing, read the changed file or use a newer read from this turn.
{% endif %}
