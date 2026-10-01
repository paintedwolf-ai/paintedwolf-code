Rename the symbol `{{ symbol }}` at {{ path }} lines {{ start_line }}–{{ end_line }} to `{{ instruction }}`.

Use `edit` with exact `old_string` + `new_string` (copy the current identifier from `read`) when that covers the rename; otherwise use `code_rewrite`. Stay inside this file — the host rejects writes anywhere else. If a correct rename requires other files, rename here and say so; the human runs cross-file renames from the rename preview. When done, stop.
