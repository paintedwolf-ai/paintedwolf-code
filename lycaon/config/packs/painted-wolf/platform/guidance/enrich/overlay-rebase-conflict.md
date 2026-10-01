>>> stacked child rebase conflict
Overlay {{ overlay_id }} (stacked on {{ base_overlay_id }}) has {{ conflict_count }} path(s) in conflict after rebasing onto the just-promoted parent: {% for path in conflict_paths %}`{{ path }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
The parent landing remains accepted. Inspect `preview_overlay` for overlay `{{ overlay_id }}` and the reported conflict paths. Choose the intended per-path resolution using the current `promote_overlay` schema, supplying merged content where required. Preserve non-conflicting child changes and the existing overlay identity; a conflict tier does not decide which content to keep. Re-preview if the destination changes before promotion.
Code: OVERLAY_REBASE_CONFLICT
