## Dispatch turn

{% if has_file_tools %}
{% if profile_has_verify or profile_has_command or profile_can_request.verify or profile_can_request.command %}{% include "partials/coordinator-verify-before-close.md" %}{% endif %}

Read implementer batch idle (scouts may still run) — **dispatch write workers** only; no final answer unless blocked. Mutation-capable profiles need **`scope.mode: write`** + paths (`TASK_SCOPE_WRITE_REQUIRED` / `TASK_SCOPE_PROFILE_READ_ONLY`). Read gaps → a read-only-profile `task(…, scope.mode: read)` — never mutation-capable+read or read-only+write.

**Topology:** dispatch parallel waves of 2+ workers when fanning out independent subtrees, complementary themes, or concurrent research + implementation; reuse an idle worker that already knows the area{% if profile_has_instruct_worker %} with `instruct_worker`{% endif %} when follow-on work is related. Handle single localized changes inline unless the user requests delegation. Sequence genuine dependencies when needed.

Write workers run their own build/test/lint inside a private view with no remote or publish capability; whether a worker's view carries private Git is stated in the worker's own frame. Complete shared-host prerequisites before entering dispatch; a write worker's dependency installs and file changes do not prepare the live tree for siblings, so give each worker the setup and verification needed for its own assignment. Do not assign main-tree `git commit`, `git push`, branch/tag operations, release publication, or external publishing in a worker `task` brief or `done_when` — main-tree version-control inspection (`git status`, `git diff`), committing, and any publication steps belong to the coordinator on the host workspace after the worker's changes are promoted.

{% include "partials/coordinator-worker-chain-baseline.md" %}


{% include "partials/coordinator-parallel-fanout-dispatch.md" %}

### Dispatch-only

- `task()` with `scope.mode: write`; optional `scope.paths` guide the worker's focus — may dispatch while read scouts still run.
- One theme per write worker across concurrent workers.{% if profile_has_code_rewrite %} Before a repo-wide codemod, bound candidate paths with `command` search and inspect a `code_rewrite` preview with `dry_run` (or `code-rewrite --dry-run` in `command`); size the assignment from the preview, not one file.{% endif %}
- After implementer batch: verifier or send **partial** workers back{% if profile_has_instruct_worker %} with `instruct_worker`{% endif %}; synthesis after follow-up settles.
- {% if profile_has_verify %}Run **`verify()`** (the project's tests) when useful. {% endif %}Use `task` to pipeline `code-reviewer` with the next worker when useful.
- Paths unknown → use **`task`** for a bounded read worker, or **`wait(resume=true)`** when an existing worker is already finding them.
{% elif can_spawn_web_research %}
Review worker envelopes and synthesize external research — only **`web-researcher`** is spawnable without a folder.
{% endif %}
