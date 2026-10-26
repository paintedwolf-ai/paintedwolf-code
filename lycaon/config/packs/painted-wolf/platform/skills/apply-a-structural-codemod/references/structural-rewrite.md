# Structural rewrite reference

The live `code_rewrite` schema is the source of truth for arguments and supported grammars. Tool confinement, write scope, mutation receipts, and rejection `Code:` values remain authoritative.

## Pattern shape

A pattern is example syntax for one complete parser node. `$VALUE` captures one node and `$$$ARGS` captures a sequence. Reuse capture names in the rewrite when their source text must survive.

```text
old_api($VALUE)      ->  new_api($VALUE)
old_api($$$ARGS)     ->  new_api($$$ARGS)
```

Use the singular capture when arity is part of the claim, and the sequence capture when arguments pass through unchanged. Include required receivers, declaration bodies, return forms, and delimiters rather than a keyword or node prefix. A bare `$A | $B` pattern is too broad and is refused.

Use text search (`rg -n`, `grep -rn`) when the target is a name, literal, comment, or formatting token rather than a syntax shape.

## Safe rewrite loop

1. Bound the scope with text search and note the candidate count.
2. Preview the pattern, paths, recursion setting, and `lang` with `dry_run: true`.
3. Compare the preview's sites and paths with the candidates.
4. Apply exactly the reviewed call.
5. Inspect the resulting diff (`git diff -- <paths>` in `command`) and verify behavior.

Preview and candidate counts can differ when the rewrite cannot render at a captured site or text search matched comments and strings. Resolve the difference before applying.

## Stop using one codemod when

- identical syntax means different things at different sites;
- the rewrite needs type, import, data-flow, or runtime knowledge the match does not establish;
- generated or vendored material dominates the matches;
- one capture needs a different rewrite depending on its contents;
- parsing is unsupported or incomplete for files in scope; or
- the preview touches any path or node outside the stated change.

Split along real semantic variants or use targeted edits. Do not compensate with a broader pattern.
