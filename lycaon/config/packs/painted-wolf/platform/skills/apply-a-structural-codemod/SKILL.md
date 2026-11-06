---
name: apply-a-structural-codemod
description: Rewrite one repeated code shape across many files with a structural pattern and native code-rewrite, previewed before it is applied.
---

# Apply a structural codemod

1. State the exact code shape to replace, the intended replacement, and the narrowest directory or file set that can contain it. Do not begin from the repository root when a package or subtree is known.
2. Use bounded text search in `command` to locate candidate paths and inspect representative source. Text matches locate candidates; they do not establish structural matches.
3. Refine the pattern until every inspected match has the same intended meaning. If sites require different semantic judgments, stop treating the work as one codemod and use bounded edits instead.
4. Preview the complete syntax pattern, replacement, and bounded paths with `code-rewrite --dry-run` in `command`. Inspect structural matches and proposed changes; repeat the preview after changing pattern or scope.
5. Review the complete preview for missed variants, duplicated captures, formatting damage, generated or vendored paths, and changes outside the requested surface. An unexpectedly empty or large preview is a reason to fix the pattern, not to apply it.
6. Apply the reviewed command without `--dry-run`. Inspect the mutation receipt and, if you hold `source_history`, `source_history(mode=mine)` for the touched paths; do not clean or rewrite unrelated dirty files.
7. Run scoped verification and required project checks through offered execution tools. Report unmatched variants or unsupported syntax as remaining scope.

See [structural rewrite patterns](references/structural-rewrite.md) for pattern construction, capture preservation, and abort conditions.
