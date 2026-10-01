---
description: >-
  Searching source for a name, literal, or code shape under a path, including
  structural tree-sitter matches.
slot: orientation
order: 23
attaches: [grep]
hosts: [coordinator, worker]
---
- **`grep`** — **When:** you need a name, literal, or code shape under a path. Text/name → RE2 always; escape literals `\(`. **Code shape** → `grep('caller($X)')`: `$VAR` runs tree-sitter AST match, not keyword OR. A structural pattern is one **whole node**, never a prefix or a regex — write the declaration as the language spells it, body included (`func $NAME($$$ARGS) $RET { $$$BODY }` in Go, `void $NAME($$$ARGS) { $$$BODY }` in Java/C); a method keeps its receiver. After `results[]`, how/what → {% if profile_has_summarize %}**`summarize`** the containing path{% else %}a scoped read{% endif %}, not match-by-match `read`.{% if profile_has_code_rewrite %} Size **`code_rewrite`** with structural grep first.{% endif %}
