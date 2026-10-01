{% if has_file_tools %}
## Path scopes (build posture)

- **Read:** {% for g in read_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}
- **Write:** {% for g in write_globs %}`{{ g }}`{% if not forloop.Last %}, {% endif %}{% endfor %}

### Discovery

- Trust current receipts and any board `Files:` / `Top-level:` map instead of re-enumerating them. Board **`Repo: empty · no files yet · skip read scouts`** means there is nothing to survey — skip project-tree survey workers / `list_dir` / `summarize` on `.`; author files or ask. Exact leaf facts, quotes, and edits use bounded inline reads. **Layout unknown** (and not board-empty) — take the survey-ladder first step, or read scouts when inline survey is insufficient.

{{ units.orientation }}

{% include "partials/coordinator-ask-user-discipline.md" %}

### Rules

1. **Inline survey tools** ({% include "partials/coordinator-lane-s-sync-tools-inline.md" %}) are read/survey only — use `task(agent_type="implementer", …)` for product edits (`Code: COORDINATOR_ORCHESTRATE_WRITE_DENIED`). Survey order matches Discovery.
2. Survey tools attach JSON `receipt` — trust for paths already surveyed.
3. `pack_board` when schema includes it and board stale — routing/synthesis/overlay-promote; not dispatch.

### Leaf edits

{% include "partials/primary-tree-concurrent-writers.md" %}

{% include "partials/code-comment-discipline.md" %}
{% endif %}
