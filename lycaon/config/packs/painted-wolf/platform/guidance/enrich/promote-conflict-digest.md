>>> promote conflict digest
{% if overlay_intent %}**Intent:** {{ overlay_intent }}{% endif %}
{% for row in conflict_digest %}
**`{{ row.path }}`**{% if row.conflict_tier %} ({{ row.conflict_tier }}){% endif %}{% if row.branch_delta_lines %}

branchΔ:
{% for line in row.branch_delta_lines %}  {{ line }}
{% endfor %}{% elif row.hunk_count %} · {{ row.hunk_count }} hunks{% endif %}{% for line in row.summary %}
  {{ line }}{% endfor %}
{% endfor %}
**Start with:** `preview_overlay({{ overlay_id }})` — inspect `ready_resolutions` and conflict hunks{% if spill_path %}; scoped `preview_overlay(path=…)` before spill at `{{ spill_path }}`{% endif %}.
`artifact` or cache noise → `drop:["path-or-folder/"]` skips a path or whole subtree. `line_shift` after a sibling landed → reconcile `branch_delta` with the current primary version using reviewed content or hunks. **`keep_theirs`** replaces the whole primary file; **`keep_ours`** discards this path's worker changes. Use either only when that whole-file choice is intended. `keep_both` needs merged content or hunks for overlapping edits. `overlapping_edit` + low hunk_count → hunk cherry-pick (`BANNER_PROMOTE_HUNK_PICK`).
Each resolution must be **that path's own** merge intent — never paste a sibling file or stale overlay branch.
Code: BANNER_PROMOTE_CONFLICT_DIGEST
