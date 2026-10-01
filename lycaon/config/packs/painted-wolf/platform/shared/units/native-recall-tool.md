---
description: >-
  Looking up what this session or its worker legs already observed instead of
  re-running a tool, or recovering a compacted tool result by its handle.
slot: orientation
order: 27
attaches: [recall]
hosts: [coordinator, worker]
---
- **`recall`** — what was already observed{% if recall_may_widen %}, this session and its worker legs, finished ones included{% else %} in this leg{% endif %}. **When:** {% if recall_may_widen %}a finished leg's envelope omits a detail the leg saw; {% endif %}a continuation record or `[compacted …]` banner names a handle whose body is gone from context;{% if recall_may_widen %} an earlier session on this project may already have looked — `widen: "project"` (`"all"` for every attached project);{% endif %} or you are about to re-run a tool only to see the same output again. Not the tree: `grep`/`find` say what files hold now, `recall` says what was seen and when.
  - Known handle: query `handle:read#3` alone, then inspect the returned body. Adding text filters the indexed excerpt and can hide a detail deeper in that body. Without a handle, narrow with `agent:` / `leg:` / `tool:` / `after:` before free text.
  - Branch on `resolution`, not the row count, and do what `next_action` says. `scope_empty` means {% if recall_may_widen %}dispatch{% else %}go observe it{% endif %}, not search again. A `changed` hit describes the file as it was.
