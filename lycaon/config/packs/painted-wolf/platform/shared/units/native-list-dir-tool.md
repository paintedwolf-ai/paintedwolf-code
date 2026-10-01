---
description: >-
  Getting a compact map of an unfamiliar repository or listing a directory to
  locate an area.
slot: orientation
order: 22
attaches: [list_dir]
hosts: [coordinator, worker]
---
- **`list_dir({"path":"."})`** gives a compact repository map for locating an unfamiliar area. A named subdirectory or explicit `max_depth`, `max_entries`, or `offset` requests a listing.{% if profile_has_summarize %} Use `summarize` to understand the area once located.{% endif %}
