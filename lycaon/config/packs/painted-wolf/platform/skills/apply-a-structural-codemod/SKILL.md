---
name: apply-a-structural-codemod
description: Change repeated code syntax across multiple sites with structural grep and code_rewrite.
---

# Apply a structural codemod

1. State the exact code shape to replace, the intended replacement, and the narrowest directory or file set that can contain it. Do not begin from the repository root when a package or subtree is known.
2. Run structural `grep` first with one complete syntax node and `$VAR` / `$$$ARGS` captures. Inspect the match count, captured bindings, paths, and representative contexts before proposing a rewrite.
3. Refine the pattern until every inspected match has the same intended meaning. If sites require different semantic judgments, stop treating the work as one codemod and use bounded edits instead.
4. Call `code_rewrite` with the same pattern and scope, the replacement template, and `dry_run: true`. Never widen the scope between search and preview without repeating the structural search.
5. Review the complete preview for missed variants, duplicated captures, formatting damage, generated or vendored paths, and changes outside the requested surface. An unexpectedly empty or large preview is a reason to fix the pattern, not to apply it.
6. Apply by repeating the reviewed call with `dry_run: false`. Inspect the mutation receipt and `source_history(mode=mine)` for the touched paths; do not clean or rewrite unrelated dirty files.
7. Run the project's scoped verification for the changed surface, then its required handoff `verify` gate. Report unmatched variants or unsupported syntax as remaining scope.

See [structural rewrite patterns](references/structural-rewrite.md) for pattern construction, capture preservation, and abort conditions.
