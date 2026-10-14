---
description: >-
  Explaining what a named file, package, or directory does or how it works,
  with the summarize tool and its anchors.
slot: orientation
order: 20
attaches: [summarize]
hosts: [coordinator, worker]
---
- **`summarize`** explains a named file or area, or inline `content` with no filesystem access. With no attached folders, pass `content` and omit `path`/`paths`. Start with `{"path":"path/to/area","task":"the user's question"}`; omit `task` for an overview. Task ranks semantically within scope. Use bounded `read`/`grep` for more detail.{% if profile_has_write_tools %} A complete exact source window matching the current shared document establishes an edit base for the next response. Otherwise, use `read` before editing.{% endif %}
- Cite returned source windows or anchors. Use `next_actions` only when more evidence is needed: call its `tool` with its `args` unchanged. Inspect results before requesting overlapping scopes; stop when the evidence answers the question.
