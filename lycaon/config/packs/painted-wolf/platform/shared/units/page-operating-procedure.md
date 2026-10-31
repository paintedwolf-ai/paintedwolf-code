---
description: >-
  Capturing, driving, measuring, or rendering pages, mockups, images, and
  video frames with the visual tools.
slot: procedures
order: 30
attaches: [capture_page, page_open, page_act, page_snapshot, page_close, measure_page, render_view, view_image, view_video]
hosts: [coordinator, worker]
---
{% if profile_has_capture_page %}- `capture_page`: bind `project_dir` (static) or loopback `url` (dev/SSR/auth), never a generic static server. Inspect image, `snapshot` bounds (`rect`, `viewport` overflow), state, and `log`; fix stretching, clipping, or overlap even when state/`log` are clean before final capture.
{% endif %}{% if profile_has_page_controls %}- Stateful flows: {% if profile_has_page_open %}open with `page_open`; {% endif %}{% if profile_has_page_snapshot %}inspect with `page_snapshot`; {% endif %}{% if profile_has_page_act %}interact with `page_act`; {% endif %}exercise flow and capture each material state{% if profile_has_page_close %}; close with `page_close`{% endif %}.
{% endif %}{% if profile_has_page_act %}- Symptom fit: brief glitch (flash, jump, shift) → `page_act` with `record` for a timeline. Data missing/stale/failing → check `network`/`errors` and stub via `route`.{% if profile_has_measure_page %} Ignored click → `measure_page` `reach` names what covers or clips it.{% endif %}
{% endif %}{% if profile_has_measure_page %}- Layout/contrast numbers require `measure_page`.
{% endif %}{% if profile_has_render_view %}- `render_view`: renders vector icons, chrome, or mockups; pass `handle` to patch with `old_string`/`new_string`, or `dest` to save.
{% endif %}{% if profile_has_view_image %}- `view_image`: inspects images (`.svg`, raster) or session handles; returns rendered pixels and text without a server.
{% endif %}{% if profile_has_view_video %}- `view_video`: frames from a recording or video at `times_ms` or across `start_ms`..`end_ms`; `crop` reads a region at full size.
{% endif %}
