>>> promote partial path
Overlay `{{ overlay_id }}` — clean: {% for p in clean_paths %}`{{ p }}`{% if not forloop.Last %}, {% endif %}{% endfor %} · conflict: {% for p in conflict_paths %}`{{ p }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
Review delivery and validation before promotion. An unresolved conflict leaves **all paths unlanded** and the overlay pending. Do not reject for conflicts, unfinished work, or validation limits: preserve wanted work and resume incomplete delivery via `task(child_session_id=…)`. Reject only wrong, superseded, duplicate, or unwanted work.
Inspect {% if spill_path %}`read(path={{ spill_path }})`{% else %}`preview_overlay({{ overlay_id }}, path=…)`{% endif %}, then resolve **every** conflicting path in one `promote_overlay(overlay_id={{ overlay_id }}, resolutions=[{path, action}])` call. `keep_theirs` intentionally replaces primary with the worker version.
Code: BANNER_PROMOTE_PARTIAL_PATH
