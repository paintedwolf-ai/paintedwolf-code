Inline edit the file at {{ path }} lines {{ start_line }}–{{ end_line }}.

Instruction from the human:
{{ instruction }}

Use only file tools. Prefer `edit` (`old_string` + `new_string`) / `replace_lines` for a local change; use `code_rewrite` when the change is structural. Stay inside the given line range unless the instruction clearly requires a slightly larger enclosing definition. Do not run shell or network tools. When done, stop — do not write a long wrap-up.
