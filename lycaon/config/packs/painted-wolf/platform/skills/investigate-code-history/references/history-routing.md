# Code history routing reference

Maintained sources of truth: the live native Git tool schemas. This workflow uses only read tools. Repository mutation tools belong to an explicitly requested change workflow.

An isolated write leg works in a copy that carries no repository metadata, so no Git tool is on its roster. Only the `source_history` rows below apply there.

## Choose the evidence

| Question | First evidence | Follow-up |
|---|---|---|
| Is the change only local? | `git_status`, then path-scoped `git_diff` | Read the changed source |
| Who changed this file during live work — me, another agent, the user? | `source_history(path)` (ledger record, covers uncommitted state) | Read the current bytes before acting on them |
| How do I revert or undo an edit to an earlier version? | `restore_version(path)` (reverts to previous retained version) or with `version_id` | Read restored file to verify |
| What did a historical uncommitted version look like? | `source_history(path, mode=version, version_id=…)` | Inspect exact retained code at that point |
| What did an uncommitted edit/version change? | `source_history(path, mode=diff, version_id=…)` | Unified diff against parent or base version |
| Who wrote these uncommitted lines? | `source_history(path, mode=lines, start_line, end_line)` | `git_blame` for the committed layer beneath |
| What has this session itself changed? | `source_history(mode=mine)` | Path-scoped `git_diff` for the byte-level view |
| Which commit last touched these lines? | Bounded `git_blame(path, start_line, end_line)` | `git_show(ref=<commit>)` |
| How did one file evolve? | `git_log(path=…)` | Show the relevant commits and current file |
| Which commits differ between branches? | `git_compare(base_ref=…, head_ref=…)` | `git_log(ref="base..head", limit=…, offset=…)` for commits; `git_diff(base_ref=…, head_ref=…)` for contents |
| What does a branch or tag name resolve to? | `git_ref(refs=[…])` | Show the resolved commit |
| What did a known commit change? | `git_show(ref=…)` | Read current code before claiming present behavior |

Use `git_branches` only to discover local branch names. Do not infer remote state, PR status, review intent, or CI state from local refs; use the GitHub workflow when that is the question.

## Attribution traps

- Blame reports the last textual touch, not necessarily the author of the behavior.
- Formatting and mechanical rewrites can replace the useful blame commit with a low-signal one.
- A commit message records an author's statement, not proof that the change achieved it.
- Renames, squashes, shallow clones, and rewritten history can make provenance incomplete.
- Uncommitted or staged changes sit outside ordinary commit history; `source_history` covers them from the host ledger, with each change actor-classified (you / agent / user / external / mixed). An absent ledger record is unknown provenance, not proof of no change.
- Old code may have been made irrelevant by a later caller, configuration, or guard.

When attribution is uncertain, report the competing commits or missing history instead of selecting one by narrative fit.
