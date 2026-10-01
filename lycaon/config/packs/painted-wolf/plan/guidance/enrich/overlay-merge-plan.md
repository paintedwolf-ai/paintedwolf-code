>>> overlay merge plan
**{{ pending_count }}** pending overlay(s) — promote in order: {% for id in promote_sequence %}`{{ id }}`{% if not forloop.Last %} → {% endif %}{% endfor %}{% if shared_paths %}

Shared paths:{% for row in shared_paths %}
- `{{ row.path }}` — {% for id in row.job_ids %}`{{ id }}`{% if not forloop.Last %}, {% endif %}{% endfor %}{% endfor %}{% endif %}
Ordering does not replace review: inspect delivery, current evidence and validation limits. Preserve unfinished work and resume its leg first.
`preview_overlay` each overlay before promote; re-preview siblings after each landing. `pack_board` refreshes this plan.
Code: BANNER_OVERLAY_MERGE_PLAN
