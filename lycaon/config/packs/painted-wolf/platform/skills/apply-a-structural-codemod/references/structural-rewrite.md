# Structural rewrite reference

Maintained sources of truth: command help for text-search syntax and the live `code_rewrite` schema for structural patterns, replacements, and supported grammars, and the project's own verification policy for completion. Tool confinement, write scope, mutation receipts, and structured rejection `Code:` values remain authoritative.

## Pattern shape

A structural pattern is example syntax representing one complete parser node. `$VALUE` captures one node and `$$$ARGS` captures a sequence. Reuse the same capture names in the replacement when their source text must survive.

```text
old_api($VALUE)
new_api($VALUE)

old_api($$$ARGS)
new_api($$$ARGS)
```

Use the singular capture when arity is part of the claim and the sequence capture when arguments should pass through unchanged. Include required receivers, declaration bodies, return forms, delimiters, and surrounding syntax rather than supplying a keyword or node prefix. Use `command` with `rg PATTERN PATH` when the target is a name, literal, comment, or formatting token rather than syntax shape.

## Safe rewrite loop

1. Bound candidate paths with text search.
2. Inspect representative source and choose one complete syntax pattern.
3. Preview that pattern, replacement, paths, recursion setting, and language with `code_rewrite(dry_run: true)`.
4. Inspect structural matches, affected paths, and proposed changes.
5. Apply exactly the reviewed call.
6. Inspect the resulting diff and verify behavior.

Text-search counts are not structural match counts. Resolve failed or unrenderable replacements in the preview before applying.

## Stop using one codemod when

- identical syntax has different meanings at different sites;
- the replacement needs type, import, data-flow, or runtime knowledge the syntax match does not establish;
- generated or vendored material dominates the matches;
- one capture would need a different replacement depending on its contents;
- parsing is unsupported or structurally incomplete for material in scope; or
- the preview touches any path or node outside the stated change.

Split along real semantic variants or use targeted edits. Do not compensate with a broader pattern.
