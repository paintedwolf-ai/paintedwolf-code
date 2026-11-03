## Dispatch turn

{% if has_file_tools %}
{% if profile_has_verify or profile_has_command or profile_can_request.verify or profile_can_request.command %}{% include "partials/coordinator-verify-before-close.md" %}{% endif %}

Read implementer batch idle (scouts may still run) — **dispatch write legs** only; no user reply unless blocked. Mutation-capable profiles need **`scope.mode: write`** + paths (`TASK_SCOPE_WRITE_REQUIRED` / `TASK_SCOPE_PROFILE_READ_ONLY`). Read gaps → a read-only-profile `task(…, scope.mode: read)` — never mutation-capable+read or read-only+write.

**Topology:** dispatch parallel waves of 2+ workers when fanning out independent subtrees, complementary themes, or concurrent research + implementation. Handle single bounded assignments inline unless the user requests delegation. Sequence genuine dependencies when needed.

Write legs run their own build/test/lint inside a private snapshot overlay with no version-control metadata (`.git` is excluded) and no remote or publish capability. Complete shared-host prerequisites before entering dispatch; a write leg's dependency installs and file changes do not prepare the live tree for siblings, so give each worker the setup and verification needed for its own leg. Do not assign `git commit`, `git push`, branch/tag operations, release publication, or external publishing in a worker `task` brief or `done_when` — version-control inspection (`git_status`, `git_diff`), committing (`git_commit`), and any publication steps belong to the coordinator on the host workspace after the worker's changes are promoted.

{% include "partials/coordinator-worker-chain-baseline.md" %}


{% include "partials/coordinator-parallel-fanout-dispatch.md" %}

### Dispatch-only

- `task()` with `scope.mode: write`; optional `scope.paths` guide the worker's focus — may dispatch while read scouts still run.
- One theme per write leg across concurrent legs.{% if profile_has_code_rewrite %} Before a repo-wide codemod, run **`structural-grep`** in `command` (no rewrite) over the target subtree and use the match count to size the leg — do not guess blast radius from one file.{% endif %}
- After implementer batch: verifier or re-dispatch **partial** legs; synthesis after follow-up settle.
- {% if profile_has_verify %}Run **`verify()`** (the project's tests) when useful. {% endif %}Use `task` to pipeline `code-reviewer` with the next leg when useful.
- No `pack_board` here; routing/synthesis/overlay-promote do.
- Paths unknown → use **`task`** for a bounded read worker, or **`wait(resume=true)`** when an existing worker is already finding them.
{% elif can_spawn_web_research %}
Review worker envelopes and synthesize external research — only **`web-researcher`** is spawnable without a folder.
{% endif %}
