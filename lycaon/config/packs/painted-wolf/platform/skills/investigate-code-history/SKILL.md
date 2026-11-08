---
name: investigate-code-history
description: Explain how and why code reached its current form from live edit history, git blame, and commit logs, including who changed which lines.
metadata:
  paintedwolf.any_of_tools: command, source_history
---

# Investigate code history

{% if has_git and not on_worker_branch %}This workspace has Git metadata, so the Git steps below run in `command`.{% if profile_has_source_history %} `source_history` adds the live source ledger for uncommitted work.{% endif %}{% else %}This leg has no Git metadata: a write overlay is a copy without `.git`, so the Git steps below do not apply. {% if profile_has_source_history %}`source_history` is the history evidence here.{% else %}Report the historical question for a read leg or the coordinator to resolve against Git metadata.{% endif %}{% endif %}

1. Name the historical question and its narrowest current anchor: a path, line range, symbol, or known ref. Read the current code first when the anchor is not in evidence.
2. {% if has_git and not on_worker_branch %}{% if profile_has_source_history %}Call `source_history()` for this session's changes, or `source_history(path)` for one file's live record. {% endif %}Use `git status --short` in `command` to distinguish committed history from local edits. A dirty tree is evidence, not cleanup work.{% else %}{% if profile_has_source_history %}Call `source_history()` for this session's changes and `source_history(path)` for one file's record; the ledger is the only history here, and a dirty overlay is evidence, not cleanup work.{% else %}Name the anchor in your report; this leg cannot separate committed from local edits.{% endif %}{% endif %}
3. {% if has_git and not on_worker_branch %}Inspect bounded paths with `git diff -- paths` in `command`; the ledger remains authoritative for live changes and their actors.{% else %}{% if profile_has_source_history %}Inspect bounded paths with `source_history(path, mode=diff)`; the ledger is authoritative for live changes.{% else %}Describe the bounded paths from overlay files and mark provenance unknown.{% endif %}{% endif %}
4. {% if profile_has_source_history %}For a hand edit, another session, or a worker promotion, use `source_history` (`mode=effects`, `mode=lines`, `mode=mine`, or `mode=diff` with no version for the change since session start). An absent record means unknown provenance.{% else %}{% if has_git and not on_worker_branch %}Treat a hand edit or another session's change as provenance to establish from Git evidence.{% else %}Treat any change you did not make as unknown provenance.{% endif %}{% endif %}
5. {% if has_git and not on_worker_branch %}Run bounded `git blame -L A,B -- path` in `command` for candidate commits. Blame is a pointer: formatting, moves, and later edits can obscure the origin.{% else %}{% if profile_has_source_history %}Use `source_history(path, mode=lines, start_line, end_line)` to attribute the lines; the ledger classifies each change's actor.{% else %}Skip line attribution here; name the lines that need it in your report.{% endif %}{% endif %}
6. {% if has_git and not on_worker_branch %}Inspect candidates with `git show ref -- path`, chronology with `git log -- path`, and named refs with `git rev-parse ref`, all in `command`; batch independent reads once candidates are known.{% else %}{% if profile_has_source_history %}Read an uncommitted version with `source_history(path, mode=version, version_id=…)` and session changes with `source_history(mode=mine)`.{% else %}Report uninspected candidates so a read leg can run the lookups.{% endif %}{% endif %}
7. Re-read the current implementation and its callers before concluding an earlier change still explains present behavior. Separate observed intent from your inference about the mechanism.
8. Report the relevant commit or ledger change, path and lines, observed change, and the smallest evidence-backed explanation. State when renames, squashes, shallow history, or local edits leave provenance uncertain.

Tracked removals and renames made through `git rm` and `git mv` appear in source history as filesystem changes as well as in Git staging; for a live deletion or move, inspect source history before looking for a commit.

See [history routing](references/history-routing.md) for tool selection and common attribution traps.
