>>> promote overlay body
Overlay `{{ overlay_id }}` retains unresolved paths. Review delivery and current validation evidence, then call **`promote_overlay({{ overlay_id }})`**. Promotion is atomic: unresolved conflicts return a menu without landing any paths.
Resolve every menu path together in one call with `resolutions:[{path, action}]` — **keep_both** (retain both intents; overlapping edits require reviewed merged `content` or `hunks`), **keep_theirs** (the worker's version), or **keep_ours** (primary unchanged). Skip accidental/generated paths with `drop:["…"]`.
{% if conflict_paths %}Conflicts this pass: {% for p in conflict_paths %}`{{ p }}`{% if not forloop.Last %}, {% endif %}{% endfor %} — the worker version lives on the overlay until you promote; a primary `read()` shows the primary version, not the pending worker changes. Use the scoped preview to compare both.{% endif %}
`reject_overlay({{ overlay_id }})` only for wrong, superseded, duplicate, or unwanted work — it is **terminal**. Preserve unfinished work and resume via `task(child_session_id=…)`; do not confuse blocked validation with incomplete delivery.
Code: BANNER_PROMOTE_OVERLAY_BODY
