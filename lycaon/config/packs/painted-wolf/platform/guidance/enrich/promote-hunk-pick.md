>>> promote hunk pick
Overlay `{{ overlay_id }}` — **overlapping_edit** on {% for p in conflict_paths %}`{{ p }}`{% if not forloop.Last %}, {% endif %}{% endfor %} ({{ hunk_count }} hunks ≤ {{ threshold }}).
Cherry-pick: `promote_overlay({{ overlay_id }}, resolutions=[{path, hunks:[{start_line, end_line, content}]}])`.
Use `conflict_digest` summary lines (`primary:` / `branch:`) — pick the side you want per hunk. Apply hunks **back-to-front** (highest `start_line` first).
One `{path, hunks}` per file — do not mix `hunks` and `content` on the same path.
Code: BANNER_PROMOTE_HUNK_PICK
