---
description: >-
  Finding where something lives in a project whose layout is unknown:
  duplicated config or stores, HTTP routes and handlers, or an overview of the
  tree.
slot: orientation
order: 21
attaches: [survey_repo]
hosts: [coordinator, worker]
---
- `survey_repo` finds a destination through one catalog map, not a briefing. Use `bundle=layout_overview` for unknown layout, `bundle=ssot_drift` for duplicated config/stores, or `bundle=api_routes` for HTTP routes/handlers; optional `path` narrows the map. Skip it when you already know the area and need to understand its behavior. After one map, {% if profile_has_summarize %}`summarize` the destination it names{% else %}read the destination it names{% endif %}; the map/digest alone does not support a code claim.
