>>> promote high conflict
Overlay `{{ overlay_id }}` has up to **{{ hunk_count }} hunks**, above the {{ threshold }}-hunk picking threshold, on {% for p in conflict_paths %}`{{ p }}`{% if not forloop.Last %}, {% endif %}{% endfor %}.
Check each path's `conflict_tier`. `line_shift` is a drift hint: compare `branch_delta` with primary; hunk count does not prove drift. `overlapping_edit` needs deliberate reconciliation of both edits.
**Start with:** `preview_overlay({{ overlay_id }})` — inspect `ready_resolutions` and conflict hunks; use `detail:"full"` or `spill_path` for source snapshots.
`promote_overlay({{ overlay_id }})` auto-reconciles mergeable edits but lands nothing while any conflict is unresolved. Supply all remaining choices together in `resolutions:[{path, action}]`, or `drop:["path-or-folder/"]` for accidental/generated material. Still blocked after `promote_sequence` + re-preview → compose merged `content` or `hunks` to retain both intents. **`keep_theirs`** replaces the entire primary file with the worker version; **`keep_ours`** discards this path's worker changes. Successful promotion lands the resolved changeset atomically.
Do **not** `task(implementer)` while overlays still pending. `reject_overlay` when overlay is superseded or no longer needed.
Code: BANNER_PROMOTE_HIGH_CONFLICT
