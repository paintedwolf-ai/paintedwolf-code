---
description: >-
  Explaining what a named file, package, or directory does or how it works,
  with the summarize tool and its anchors.
slot: orientation
order: 20
attaches: [summarize]
hosts: [coordinator, worker]
---
- `summarize` explains a named file or area, or inline `content` with no filesystem access. Start with `path` and a `task` holding the user's question; omit `task` for an overview. With no attached folders, pass `content` and omit `path` and `paths`. Use `outline --path FILE` in `command` for file structure.
- A briefing orients and its `command#` handle proves only that it ran. Read with `cat -n` or `grep -n` to quote, edit, or cite lines. Follow up only where the evidence leaves the task unresolved.
