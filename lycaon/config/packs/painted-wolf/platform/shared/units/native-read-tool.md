---
description: >-
  Reading exact lines of a text file for a verbatim quote, a symbol, or an
  anchor before editing, including outline mode for large files.
slot: orientation
order: 26
attaches: [read]
hosts: [coordinator, worker]
---
{% set read_line_limit = coordinator_read_line_limit|default:worker_read_line_limit %}
- **`read`** — **When:** verbatim quote, symbol, or edit anchor. Text files only;{% if profile_has_view_image %} images and SVGs → `view_image`.{% endif %} Numbered output (`     1: text`) — text after `: ` anchors {% if profile_has_write_tools %}`edit`/`replace_lines`{% endif %}. Max {{ read_line_limit }} lines.{% if profile_has_jq %} Docs: **`jq`** for keys; **`read`** for anchors.{% endif %} How/what over dir → {% if profile_has_summarize %}**`summarize(path=…)`**{% else %}outline, then range{% endif %}.
- Unscoped `read` may return the whole file when small. **Larger** → `mode: outline`; for how/what call {% if profile_has_summarize %}**`summarize`** next{% else %}`symbol=` / `ranges`{% endif %}; use `symbol=`/`ranges` for quotes, then `edit` with the exact text after `: `. **`mode: outline`** after edits for parse health — advisory; verify is authoritative.
