Rename the symbol `{{ symbol }}` at {{ path_display }} lines {{ start_line }}–{{ end_line }} to `{{ instruction }}`.

Work from the attached selection; inspect the current identifier through `command` only when context beyond the selection is needed, then apply a scoped patch or structural rewrite through `command`. Stay inside this file — the host rejects writes anywhere else. If a correct rename requires other files, rename here and say so; the human runs cross-file renames from the rename preview. When done, stop.
