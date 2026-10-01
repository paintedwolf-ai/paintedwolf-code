# commentlint

`commentlint` is the repository's source-comment scanner. Parallel lexers cover the repository's active languages. Potential findings and recognized languages without a lexer are validated against the language syntax tree. Diff files use their complete line-prefix comment grammar directly; added, deleted, and context lines remain patch content.

## Repository use

Run the configured checks from the repository root:

```sh
./task comments:check
```

`./task check` includes this task for CI and release gates.

Directory traversal stops at nested checkouts identified by a `.git` file or directory. To scan one separately, select that checkout with `--root`.

## Rules

- `line-length` limits the longest line in a comment.
- `work-marker` requires `TODO`, `FIXME`, `HACK`, and `XXX` comments to carry an issue reference.
- `local-home-path` rejects the current machine's home directory.
- `forbid:<name>` applies an explicitly configured regular expression.

Suppress one finding inside its comment with `commentlint:allow <rule>`. Suppressions remain visible in source and apply only to the named rule.

## Configuration

Pass `--config <path>` to load JSON relative to the repository root. Command-line scalar options override configured values. Command-line exclusions and forbidden rules extend the configured lists.

```json
{
  "max_line_length": 120,
  "work_markers_require_issue": true,
  "issue_pattern": "(?:#[0-9]+|[A-Z][A-Z0-9]+-[0-9]+)",
  "local_home_paths": true,
  "parse_timeout": "30s",
  "excludes": ["generated"],
  "forbid": {
    "private-marker": "INTERNAL ONLY"
  }
}
```

Use `--json` for machine-readable findings, elapsed time, and language coverage. Unsupported extensions and parse timeouts are reported separately. Findings and timeouts fail the command unless `--report-only` is set.
