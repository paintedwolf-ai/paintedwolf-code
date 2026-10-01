Security delta observed — a background scan of files that changed in this project finished.{% if scanner_id %} Scanner `{{ scanner_id }}`{% endif %}{% if scan_id %}, scan `{{ scan_id }}`{% endif %}: **{{ introduced_count }} introduced**, **{{ fixed_count }} fixed**.
{% if introduced_count > 0 %}
Introduced by recent edits:
{% for f in introduced %}
  • {{ f.level }} {{ f.rule_id }} — {{ f.path }}{% if f.line %}:{{ f.line }}{% endif %}{% if f.message %} — {{ f.message }}{% endif %}
{% endfor %}{% if introduced_omitted_count > 0 %}
  … {{ introduced_omitted_count }} more; use `scan_query` with `introduced_since` for the full list.
{% endif %}{% endif %}{% if fixed_count > 0 %}
Fixed by recent edits:
{% for f in fixed %}
  • {{ f.level }} {{ f.rule_id }} — {{ f.path }}{% if f.line %}:{{ f.line }}{% endif %}
{% endfor %}{% if fixed_omitted_count > 0 %}
  … {{ fixed_omitted_count }} more.
{% endif %}{% endif %}
This scan did not block your work and nothing waits on it. Fix an introduced finding when it belongs to the task at hand; otherwise note it and continue. `scan_query` reads the details; a full pass runs only when asked for with `scan_pack`.
