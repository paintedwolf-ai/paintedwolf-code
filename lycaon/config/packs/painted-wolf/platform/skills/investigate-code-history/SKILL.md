---
name: investigate-code-history
description: Explain how and why code reached its current form from live edit history, git blame, and commit logs, including who changed which lines.
metadata:
  paintedwolf.any_of_tools: command, source_history
---

# Investigate code history

On a write leg, which has no Git metadata, the `git` steps below and the Git rows of the routing reference do not apply. If you hold `source_history`, `source_history()` and `source_history(path, mode=diff)` answer them there.

1. Name the historical question and its narrowest current anchor: a path, line range, symbol, or known ref. Read the current code first when the anchor is not already in evidence.
2. Call `source_history()` first for what changed this session, or `source_history(path)` for one file's live and uncommitted record. Use `git status --short` in `command` to distinguish committed history from local edits. A dirty tree is evidence, not cleanup work.
3. Inspect bounded paths with `git diff -- paths` in `command`. The source ledger remains authoritative for live changes and their actors.
4. For a hand edit, another session, or a worker promotion, use `source_history` (`mode=effects`, `mode=lines`, `mode=mine`, or `mode=diff` with no version for a file's change since the session began). An absent record means unknown provenance.
5. Run bounded `git blame -L A,B -- path` in `command` to identify candidate commits. Blame is a pointer: formatting, moves and later edits can obscure the originating change.
6. Inspect candidates with `git show ref -- path`, chronology with `git log -- path`, and named refs with `git rev-parse ref`, all in `command`. Batch independent reads after the candidates are known.
7. Re-read the current implementation and its callers before concluding that an old commit still explains present behavior. Separate observed commit intent from your inference about the mechanism.
8. Report the relevant commit hash, path and lines, observed change, and the smallest evidence-backed explanation. State when renames, squashes, shallow history, or local edits leave provenance uncertain.

Tracked removals and renames made through `git rm` and `git mv` appear in source history as filesystem changes as well as in Git staging. For recovery of a live deletion or move, inspect source history before looking for a commit.

See [history routing](references/history-routing.md) for tool selection and common attribution traps.
