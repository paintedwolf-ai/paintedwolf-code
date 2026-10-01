>>> promote spill first
Overlay `{{ overlay_id }}` — **line_shift** / offset drift{% if spill_path %} · spill at `{{ spill_path }}`{% endif %}{% if branch_delta_lines %} · branchΔ:
{% for line in branch_delta_lines %}  {{ line }}
{% endfor %}{% endif %}.
**Start with:** `preview_overlay({{ overlay_id }})` — use `ready_resolutions`; `drop:["path-or-folder/"]` skips an artifact path or whole subtree in one promote pass.
Scoped `preview_overlay(path=…)` when reconcile confidence is low. **Do not** loop `read()` on whole primary product files.
`reject_overlay` when the overlay is superseded or no longer needed — do not leave stale overlays pending.
Remaining `needs_review: true` → `promote_overlay(resolutions=[{path, action}])`.
Code: BANNER_PROMOTE_SPILL_FIRST
