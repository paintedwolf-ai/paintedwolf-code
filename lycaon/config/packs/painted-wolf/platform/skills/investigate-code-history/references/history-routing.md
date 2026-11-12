# Code history routing reference

{% if has_git and not on_worker_branch %}Native Git commands in `command` provide committed history.{% if profile_has_source_history %} `source_history` provides the live source ledger for uncommitted work.{% endif %}{% else %}This leg has no Git metadata: a write overlay is a copy without `.git`, so Git rows do not apply here. {% if profile_has_source_history %}`source_history()` and `source_history(path, mode=diff)` replace `git status` and `git diff` here.{% else %}Report the historical question for a read leg or the coordinator to resolve against Git metadata.{% endif %}{% endif %}

## Choose the evidence

| Question | Evidence |
|---|---|
{% if has_git and not on_worker_branch %}| Is the change only local? | `git status --short` and `git diff -- path` in `command` |
| Which commit last touched these lines? | `git blame -L A,B -- path`, then `git show ref -- path` in `command` |
| How did a file evolve? | `git log -- path` in `command` |
| Which commits differ between branches? | `git rev-list --left-right --count base...head`, `git log base..head`, and `git diff base head -- paths` in `command` |
| What does a ref resolve to? | `git rev-parse ref` in `command` |
| Where did text exist at an older revision? | `command`: `git grep -n PATTERN REV -- PATH`; `git show REV:PATH` for the whole file |
| Which release contains a commit? | `command`: `git describe --tags --always REV`; `git log` between the named tags |
| Where did a branch point before a reset or rebase? | `command`: `git reflog -n 20 [REF]`; `git show` the earlier entry before restoring anything |
| Which files or objects exist at a revision? | `command`: `git ls-tree -r --name-only REV`, `git cat-file -t`/`-p REV:PATH`; Read the current file before comparing |
{% endif %}{% if profile_has_source_history %}| What changed on this branch this session, by anyone? | `source_history()` |
| What has this file changed since the session began? | `source_history(path, mode=diff)` |
| Who changed this file during live work? | `source_history(path)` |
| What did an uncommitted version contain or change? | `source_history(path, mode=version, version_id=…)` or `mode=diff` with `version_id` |
| Who wrote these uncommitted lines? | `source_history(path, mode=lines, start_line, end_line)` |
| What has this session changed? | `source_history(mode=mine)` |
{% endif %}{% if profile_has_restore_version %}| How do I undo a retained file change? | `restore_version(path)` or with `version_id`, only when requested |{% else %}| How do I undo a retained file change? | Not available on this reader — record the file and version in your report |{% endif %}
{% if has_git and not on_worker_branch %}
Discover local branches with `git branch --list` in `command`. Local refs do not establish remote state, PR status or CI results.
{% endif %}
## Attribution traps

- Blame reports the last textual touch, not necessarily the author of the behavior.
- Formatting and mechanical rewrites can replace the useful blame commit with a low-signal one.
- A commit message records an author's statement, not proof that the change achieved it.
- Renames, squashes, shallow clones, and rewritten history can make provenance incomplete.
- Uncommitted or staged changes sit outside ordinary commit history{% if profile_has_source_history %}; `source_history` covers them from the host ledger, with each change actor-classified (you / agent / user / external / mixed). An absent ledger record is unknown provenance, not proof of no change{% endif %}.
- Old code may have been made irrelevant by a later caller, configuration, or guard.

When attribution is uncertain, report the competing commits or missing history instead of selecting one by narrative fit.
