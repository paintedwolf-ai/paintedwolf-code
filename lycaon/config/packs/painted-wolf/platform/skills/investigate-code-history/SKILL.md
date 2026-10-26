---
name: investigate-code-history
description: Explain how and why code reached its current form from live edit history, git blame, and commit logs, including who changed which lines.
optional_tools:
  - git_compare
---

# Investigate code history

1. Name the historical question and its narrowest current anchor: a path, line range, symbol, or ref. Read the current code first when the anchor is not already in evidence.
2. Call `source_history(path)` first for live and uncommitted changes, then `git status --short -- <path>` in `command` to separate committed history from local edits. A dirty tree is evidence, not cleanup work; never restore, stash, or commit it for an investigation.
3. For who changed a file during live work (a hand edit, another session, a worker promotion), use `source_history`: `mode=effects` for the change record, `mode=lines` for per-line authorship, `mode=mine` for this session's footprint. An absent record is unknown provenance, never "unchanged".
4. For the committed layer, run bounded blame: `git blame -L 120,160 -- internal/auth/session.go`. Blame is a pointer; formatting, moves, and later edits can hide the originating change.
5. Inspect candidates in one program, not one commit per turn:

   ```bash
   git show --stat <sha1> <sha2>
   git log --oneline -n 20 --follow -- internal/auth/session.go
   git show <sha1> -- internal/auth/session.go
   ```

6. Re-read the current implementation and its callers before concluding that an old commit explains present behavior. Separate the commit's stated intent from your inference about the mechanism.
7. Report the commit hash, path and lines, the observed change, and the smallest evidence-backed explanation. Say when renames, squashes, shallow history, or local edits leave provenance uncertain.

Question-to-evidence table and attribution traps: [history-routing.md](references/history-routing.md).
