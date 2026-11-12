---
name: investigate-code-history
description: Explain how and why code reached its current form from live edit history, git blame, and commit logs, including who changed which lines.
metadata:
  paintedwolf.any_of_tools: command, source_history
---

# Investigate code history

{% if has_git and not on_worker_branch %}This workspace has Git metadata; the Git steps below run in `command`.{% if profile_has_source_history %} `source_history` adds the live ledger for uncommitted work.{% endif %}{% else %}This leg has no Git metadata: a write overlay is a copy without `.git`, so Git steps do not apply. {% if profile_has_source_history %}`source_history` is the history evidence here.{% else %}Report the historical question for a Git-metadata leg to resolve.{% endif %}{% endif %}

1. Name the historical question and its narrowest anchor: a path, line range, symbol, or known ref. Read the current code first when the anchor is not in evidence.
2. {% if has_git and not on_worker_branch %}{% if profile_has_source_history %}Call `source_history()` for this session's changes or `source_history(path)` for one file's record. {% endif %}Use `git status --short` in `command` to separate committed history from local edits. A dirty tree is evidence, not cleanup work.{% else %}{% if profile_has_source_history %}Call `source_history()` for this session's changes and `source_history(path)` for one file's record; the ledger is the only history here; a dirty overlay is evidence, not cleanup work.{% else %}Name the anchor in your report; this leg cannot separate committed from local edits.{% endif %}{% endif %}
3. {% if has_git and not on_worker_branch %}Inspect bounded paths with `git diff -- paths` in `command`; the ledger stays authoritative for live changes and actors.{% else %}{% if profile_has_source_history %}Inspect bounded paths with `source_history(path, mode=diff)`; the ledger is authoritative for live changes.{% else %}Describe the bounded paths from overlay files and mark provenance unknown.{% endif %}{% endif %}
4. {% if profile_has_source_history %}For a hand edit, another session, or a worker promotion, use `source_history` (`mode=effects`, `mode=lines`, `mode=mine`, or `mode=diff` with no version for changes since session start). An absent record means unknown provenance.{% else %}{% if has_git and not on_worker_branch %}Treat a hand edit or another session's change as provenance to establish from Git evidence.{% else %}Treat any change you did not make as unknown provenance.{% endif %}{% endif %}
5. {% if has_git and not on_worker_branch %}Run bounded `git blame -L A,B -- path` in `command` for candidate commits.{% else %}{% if profile_has_source_history %}Use `source_history(path, mode=lines, start_line, end_line)` to attribute lines; the ledger classifies each change's actor.{% else %}Skip line attribution here; name the lines that need it in your report.{% endif %}{% endif %}
6. {% if has_git and not on_worker_branch %}Inspect candidates with `git show ref -- path`, chronology with `git log -- path`, and named refs with `git rev-parse ref`, all in `command`; batch independent reads.{% else %}{% if profile_has_source_history %}Read an uncommitted version with `source_history(path, mode=version, version_id=…)` and session changes with `source_history(mode=mine)`.{% else %}Report uninspected candidates so a read leg can run the lookups.{% endif %}{% endif %}
7. Re-read the current implementation and its callers before concluding an earlier change explains present behavior. Separate observed intent from your inference about the mechanism.
8. Report the relevant commit or ledger change, path and lines, observed change, and the smallest evidence-backed explanation. State when renames, squashes, shallow history, or local edits leave provenance uncertain.

{% if has_git and not on_worker_branch %}Tracked removals and renames through `git rm` and `git mv` appear in source history as well as in Git staging. {% endif %}{% if profile_has_source_history %}For a live deletion or move, inspect source history before looking for a commit.{% else %}For a live deletion or move, report what the change removed so a reader with the ledger or Git metadata can confirm it.{% endif %}

See [history routing](references/history-routing.md) for tool selection and common attribution traps.
