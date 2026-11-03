---
description: >-
  Finding where something lives in a project whose layout is unknown:
  duplicated config or stores, HTTP routes and handlers, or an overview of the
  tree.
slot: orientation
order: 21
attaches: [command]
hosts: [coordinator, worker]
---
- `repo-map --path DIR` in `command` finds a destination through a layout map. Use scoped text search for routes or repeated configuration. Skip it when you already know the area and need to understand its behavior. After one map, {% if profile_has_summarize %}`summarize` the destination it names{% else %}read the destination it names{% endif %}; the map/digest alone does not support a code claim.
