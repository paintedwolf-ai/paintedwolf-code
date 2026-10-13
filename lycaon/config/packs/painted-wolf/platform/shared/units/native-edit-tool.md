---
description: >-
  Creating, editing, or reverting project files: write, edit, replace_lines,
  restore_version, and bulk code rewrites, with the syntax-health rules that
  apply.
slot: orientation
order: 30
attaches: [write, edit, replace_lines, restore_version, code_rewrite]
hosts: [coordinator, worker]
---
Land supported source with an explicit syntax-health result. New/clean files must finish clean; an already-broken file accepts only a strictly healthier incremental repair, while promotion requires the final merge plan to be clean. Parser timeout/failure rejects. Do not stack `replace_lines` on stale line numbers.

- **New file** → **`write`**. **Existing span** → **`edit`** (call `read` in a prior response; same-batch rejects; copy exact source into `old_string`; preserve whitespace and omit host framing from both edit strings). **`replace_lines`** only for a range returned by `read` in a prior response.
- **Undo / revert a file** → **`restore_version`** (`path`, optional `version_id`). Reverts to prior ledger version or cleanly removes newly authored files. Prefer over hand-reverting when backing out changes.
- Long files: outline → **`summarize`** / **`grep`** → scoped **`read(ranges=…)`**. Re-anchor from the receipt; pre-edit line numbers go stale.
- **`replace_lines`** deletes `start_line`–`end_line` inclusive — re-emit boundary lines. For coupled seams, use one `operations` batch; every range uses the same original snapshot and lands atomically. `shift_indent` adds or removes one exact whitespace prefix across nonblank lines without re-emitting their bodies.
{% if profile_has_code_rewrite %}- Same shape at many sites → one **`code_rewrite`** with `path` or `paths[]`; preview `dry_run`. A bare `$A | $B` matches every binary or — narrow one operand.
{% endif %}{% include "partials/large-file-write-discipline.md" %}
