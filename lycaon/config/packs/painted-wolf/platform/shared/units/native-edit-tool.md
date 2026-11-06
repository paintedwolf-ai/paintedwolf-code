---
description: >-
  Creating, editing, or reverting project files through command and retained
  source versions, with syntax-health rules.
slot: orientation
order: 30
attaches: [command, restore_version]
hosts: [coordinator, worker]
---
Publish supported source with a syntax-health result. New/clean files finish clean; broken files require strictly healthier repairs. Promotion needs a clean final merge plan.

- Inspect the relevant source before editing. Use quoted here-documents or `printf` redirects for new files; `edit --path FILE --old-string OLD --new-string NEW` for an exact span of existing content, or `replace-lines`, `sed`, `awk`, `patch`, or `git apply`. On a conflict, inspect current state before retrying.
- `restore_version` restores a retained ledger version or removes a newly authored file; prefer it to hand-reverting.
- For large files, use `outline`, `summarize`, or bounded `grep -n` and `sed -n` before changing scoped source. Line numbers from `cat -n` or `grep -n` are not file content: never copy them into new text.
- Use `CMD --help` for native command forms. Scope structural rewrites narrowly.
{% include "partials/large-file-write-discipline.md" %}
