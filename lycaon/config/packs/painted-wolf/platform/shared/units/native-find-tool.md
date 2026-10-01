---
description: >-
  Locating a file by name or glob when the path is not known.
slot: orientation
order: 24
attaches: [find]
hosts: [coordinator, worker]
---
- **`find`** — **When:** you need a filename you cannot already say. A named file/package/dir + how/what is {% if profile_has_summarize %}**`summarize(path=…)`**{% else %}a scoped read{% endif %}, not `name_glob: *.py`. Walk under `path`. Put `type` / `name_glob` / depth / paging as sibling keys, never inside `path`. Pass **`name_glob` on the first call**.
- After `results[]`, how/what → {% if profile_has_summarize %}**`summarize(path=…)`**{% else %}a scoped read{% endif %}, not a read-per-match chain. Unscoped root walk only when you cannot name a directory (digest / `FIND_OVERFLOW_NARROW`).
