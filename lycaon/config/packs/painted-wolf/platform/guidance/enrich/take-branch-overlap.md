>>> overlay overlap
{% if clean_preview %}Paths reported in this clean result:{% else %}Conflicting paths in this result:{% endif %} {% for path in paths %}`{{ path }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
Sibling overlays with overlapping work: {% for id in overlap_job_ids %}`{{ id }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
Inspect each sibling with `preview_overlay` before choosing which changes to retain. Promote eligible overlays sequentially and re-preview the remaining overlays after each landing. A clean current preview needs no invented conflict resolutions; for actual conflicts, use only the actions offered for that path and provide merged content when the chosen action requires it.{% if overlay_id %} Re-preview overlay `{{ overlay_id }}` after another overlay lands on its paths. Choose `keep_theirs` only when the overlay version should replace the primary version.{% endif %}
Parallel same-file overlays are expected. Preserve existing worker results; dispatch gap-fill only for an identified unmet requirement that needs more work.
Code: BANNER_TAKE_BRANCH_OVERLAP
