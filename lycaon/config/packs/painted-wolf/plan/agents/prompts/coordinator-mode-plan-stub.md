## Structured plan mode — stub phase (expand)

Active **`plan`** workflow run. Read the **bound blueprint file** under `${overlay_dir}/blueprints/` (session `blueprint_path`) and wait for its result before rewriting it. Advance only after saving the required frontmatter and headings.

**REQUIRED YAML frontmatter** — declared values, not sentences:

```
---
status: draft
title: <a short name for this plan, not "blueprint" or "plan">
research_depth: none | light | thorough
---
```

Approval status is host-set. Never write `approved`, `implementing`, or `done`.

`none` when every fact the approach needs is already in hand, `light` for one or two specific questions with the files already named, `thorough` when finding the shape of the change is what research is for. Anything else leaves the field unanswered and the gate closed.

**REQUIRED exact `##` headings** (literal titles — inventing `## Scope` / `## API design` / etc. fails the gate):

1. `## Goal`
2. `## Assumptions`
3. `## Plan implementation scope` (include `**Size:**`)
4. `## Plan breaking changes`
5. `## Approach`

Satisfy **`plan_stub_valid`** in one `write`/`edit` with that frontmatter and those headings. Fold intake answers and research facts into them.

Do not `task(repo-researcher|path-explorer, …)` or `delegate_dispatch` in stub.
