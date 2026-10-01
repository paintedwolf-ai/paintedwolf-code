---
description: >-
  Querying the fields or shape of a JSON, YAML, or TOML file.
slot: orientation
order: 25
attaches: [jq]
hosts: [coordinator, worker]
---
- **`jq`** — **When:** querying JSON/YAML/TOML. `query: "."` inspects shape; narrow query (or `limit`/`offset`) for literal `values[]`.{% if profile_has_jq_edit %} Updates: **`jq_edit`**.{% endif %}
