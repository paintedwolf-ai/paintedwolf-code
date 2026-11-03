---
description: >-
  Creating, editing, or reverting project files: command, edit, replace_lines,
  restore_version, and bulk code rewrites, with the syntax-health rules that
  apply.
slot: orientation
order: 30
attaches: [command, edit, replace_lines, restore_version, code_rewrite]
hosts: [coordinator, worker]
---
Publish supported source with a syntax-health result. New/clean files finish clean; broken files require strictly healthier repairs. Promotion needs a clean final merge plan. Parser failure/timeout rejects.

- **New files:** quoted here-documents or `printf` redirects in `command`. **Existing spans:** `edit` with exact `old_string`, preserving whitespace and excluding host framing. Inspect source via `command` or `summarize` in a prior response; reads from the same response cannot anchor edits. `replace_lines` also requires prior inspected ranges.
- **Undo:** `restore_version` (`path`, optional `version_id`) restores a retained ledger version or removes a newly authored file; prefer it to hand-reverting.
- **Long files:** outline → `summarize` or bounded `command` (`grep -n`, `sed -n`) → scoped source. Re-anchor from current receipts after edits.
- **`replace_lines`:** inclusive `start_line`–`end_line`; re-emit boundary lines. One `operations` batch shares the original snapshot and lands atomically. `shift_indent` adds/removes one exact prefix on nonblank lines without re-emitting bodies.
{% if profile_has_code_rewrite %}- Repeated syntax → `code_rewrite` with `path` or `paths[]`; preview `dry_run`. Bare `$A | $B` matches every binary or: narrow an operand.
{% endif %}{% include "partials/large-file-write-discipline.md" %}
