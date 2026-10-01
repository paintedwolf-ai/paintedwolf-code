>>> promote scoped hunks
Overlay `{{ overlay_id }}` path `{{ path }}` — **{{ hunk_count }} hunks** inline in `conflicts[].hunks` (scoped `path=` preview; inline limit {{ inline_threshold }}).
Cherry-pick only when `conflict_tier` is `overlapping_edit` and hunk_count ≤ {{ threshold }} — apply `promote_overlay(resolutions=[{path, hunks}])` **back-to-front**.
For `line_shift`, prefer reviewed `ready_resolutions` or full-file `{path, content}`{% if spill_path %}; fuller bodies are at `{{ spill_path }}`{% endif %} — do not hand-merge via primary `read()` loops.
Code: BANNER_PROMOTE_SCOPED_HUNKS
