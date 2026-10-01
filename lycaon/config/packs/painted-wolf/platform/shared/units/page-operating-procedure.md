---
description: >-
  Capturing, driving, measuring, or rendering pages, mockups, images, and
  video frames with the visual tools.
slot: procedures
order: 30
attaches: [capture_page, page_open, page_act, page_snapshot, page_close, measure_page, render_view, view_image, view_video]
hosts: [coordinator, worker]
---
{% if profile_has_capture_page %}- `capture_page`: bind `project_dir` for static exports or loopback `url` for dev/SSR/auth. Inspect structural state and console logs; fix issues before final capture. Never use a generic static server.
{% endif %}{% if profile_has_page_controls %}- Stateful flows: {% if profile_has_page_open %}open with `page_open`; {% endif %}{% if profile_has_page_snapshot %}inspect with `page_snapshot`; {% endif %}{% if profile_has_page_act %}interact with `page_act`; {% endif %}exercise flow and capture each material state{% if profile_has_page_close %}; close with `page_close`{% endif %}.
{% endif %}{% if profile_has_page_act %}- Match the tool to the symptom. Looks wrong only briefly (flash, jump, late banner, shift after load): `page_act` with `record` returns a timeline a settled screenshot cannot show. Data missing, stale, or erroring: read the result's `network` and `errors`, and reproduce a backend state with `route` fixtures.{% if profile_has_measure_page %} A control ignores clicks: `measure_page` `reach` names what covers or clips it.{% endif %}
{% endif %}{% if profile_has_measure_page %}- Layout/contrast numbers require `measure_page`.
{% endif %}{% if profile_has_render_view %}- `render_view`: renders vector icons, chrome, or mockups; pass `handle` to patch with `old_string`/`new_string`, or `dest` to save.
{% endif %}{% if profile_has_view_image %}- `view_image`: inspects images (`.svg`, raster) or session handles; returns rendered pixels and text without a server.
{% endif %}{% if profile_has_view_video %}- `view_video`: frames from a recording or attached video, at `times_ms` or spread across `start_ms` to `end_ms`; `crop` reads a region at full size.
{% endif %}
