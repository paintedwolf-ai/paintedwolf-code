Rename the symbol `{{ symbol }}` at {{ path }} lines {{ start_line }}–{{ end_line }} to `{{ instruction }}`.

Inspect the current identifier through `command`, then apply a scoped patch or structural rewrite through `command`. Stay inside this file — the host rejects writes anywhere else. If a correct rename requires other files, rename here and say so; the human runs cross-file renames from the rename preview. When done, stop.
