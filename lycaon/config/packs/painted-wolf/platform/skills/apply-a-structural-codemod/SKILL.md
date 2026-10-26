---
name: apply-a-structural-codemod
description: Rewrite one repeated code shape across many files with a syntax-aware code_rewrite pattern, previewed before it is applied.
---

# Apply a structural codemod

1. State the exact code shape to replace, the replacement, and the narrowest directory or file set that can contain it. Start from a known package or subtree, not the repository root.
2. Find candidate sites with text search in `command`, e.g. `rg -n 'old_api\(' internal/billing`. Record the count and paths. This bounds the scope; it is not the structural match.
3. Call `code_rewrite` with one complete syntax node as `pattern` (`$VAR` captures one node, `$$$ARGS` a sequence), the `rewrite` template, the scope from step 2, and `dry_run: true`. The preview is the structural match set.
4. Compare the preview with step 2. Every changed site must have the same intended meaning; check missed variants, duplicated captures, formatting damage, and generated or vendored paths. An empty or unexpectedly large preview means the pattern is wrong. Fix it and preview again; never widen the scope between preview and apply.
5. If sites need different semantic judgments, stop treating the work as one codemod and use targeted edits.
6. Apply by repeating the reviewed call with `dry_run: false`. Inspect the result's changed, blocked, and failed lists, and `source_history(mode=mine)` for the touched paths. Leave unrelated dirty files alone.
7. Run the project's scoped verification for the changed surface, then any required `verify` gate.
8. Report the pattern, files changed, sites left unmatched, and unsupported syntax as remaining scope.

Pattern construction and stop conditions: [structural-rewrite.md](references/structural-rewrite.md).
