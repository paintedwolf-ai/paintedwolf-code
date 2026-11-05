---
description: >-
  Explaining what a named file, package, or directory does or how it works,
  with the summarize tool and its anchors.
slot: orientation
order: 20
attaches: [command]
hosts: [coordinator, worker]
---
- Use `summarize --path PATH --focus 'question'` in `command` for a bounded source briefing; omit `--focus` for an overview. Use `repo-map --path DIR` for layout and `outline --path FILE` for file structure. Native `--help` lists supported forms.
- A briefing orients and its `command#` handle proves only that it ran. Read with `cat -n` or `grep -n` to quote, edit, or cite lines. Follow up only where the evidence leaves the task unresolved.
