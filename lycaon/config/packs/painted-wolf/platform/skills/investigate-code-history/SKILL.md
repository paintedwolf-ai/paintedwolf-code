---
name: investigate-code-history
description: Trace code changes across live source history and Git commits, including authorship and line history.
optional_tools:
  - git_status
  - git_diff
  - git_blame
  - git_show
  - git_log
  - git_ref
  - git_compare
---

# Investigate code history

1. Name the historical question and its narrowest current anchor: a path, line range, symbol, or known ref. Read the current code first when the anchor is not already in evidence.
2. Call `source_history(path)` first for live and uncommitted changes. When `git_status` is available, use it once to distinguish committed history from local edits. A dirty tree is evidence, not cleanup work; never restore, stash, or commit it for an investigation.
3. When `git_diff` is available, inspect bounded paths before searching commit history. The source ledger remains authoritative for live changes and their actors.
4. When the question is who or what changed a file during live work — a hand edit, another session, a worker promotion — use `source_history` (`mode=effects` for the file's change record, `mode=lines` for per-line authorship, `mode=mine` for this session's own footprint). Treat an absent record as unknown provenance, never as "unchanged".
5. When available, use bounded `git_blame` to identify candidate commits for current lines. Treat blame as a pointer because formatting, moves, and later edits can obscure the originating change.
6. When available, inspect candidate commits with `git_show`, use path-scoped `git_log` for chronology, and resolve named refs with `git_ref`. Once blame has named the candidates, these are independent reads: `git_show` every candidate and the path-scoped `git_log` in one response, not one commit per turn.
7. Re-read the current implementation and its callers before concluding that an old commit still explains present behavior. Separate observed commit intent from your inference about the mechanism.
8. Report the relevant commit hash, path and lines, observed change, and the smallest evidence-backed explanation. State when renames, squashes, shallow history, or local edits leave provenance uncertain.

See [history routing](references/history-routing.md) for tool selection and common attribution traps.
