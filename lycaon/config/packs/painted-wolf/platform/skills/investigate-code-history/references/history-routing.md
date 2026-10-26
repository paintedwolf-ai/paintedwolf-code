# Code history routing reference

This workflow only reads. Repository mutations belong to an explicitly requested change workflow. Git forms run in `command`; `CMD --help` lists the supported options.

An isolated write leg works in a copy without repository metadata, so Git commands do not apply there. Only the `source_history` rows below do.

## Choose the evidence

| Question | First evidence | Follow-up |
|---|---|---|
| Is the change only local? | `git status --short`, then `git diff -- <path>` | Read the changed source |
| Who changed this file during live work: me, another agent, the user? | `source_history(path)` (ledger record, covers uncommitted state) | Read the current bytes before acting on them |
| How do I undo an edit to an earlier version? | `restore_version(path)`, or with `version_id` | Read the restored file to verify |
| What did a retained uncommitted version look like? | `source_history(path, mode=version, version_id=…)` | Inspect the exact retained code |
| What did an uncommitted edit change? | `source_history(path, mode=diff, version_id=…)` | Unified diff against parent or base |
| Who wrote these uncommitted lines? | `source_history(path, mode=lines, start_line, end_line)` | `git blame -L` for the committed layer beneath |
| What has this session changed? | `source_history(mode=mine)` | `git diff -- <paths>` for bytes |
| Which commit last touched these lines? | `git blame -L <start>,<end> -- <path>` | `git show <sha>` |
| How did one file evolve? | `git log --oneline --follow -- <path>` | Show the relevant commits and the current file |
| Which commits differ between branches? | `git log --oneline base..head` | `git diff base...head -- <paths>`; `git_compare` when loaded |
| What does a branch or tag resolve to? | `git rev-parse <ref>` | `git show --stat <sha>` |
| What did a known commit change? | `git show --stat <sha>`, then `git show <sha> -- <path>` | Read current code before claiming present behavior |

`git branch --list` discovers local branch names only. Do not infer remote state, PR status, review intent, or CI state from local refs; use work-with-github for those.

## Attribution traps

- Blame reports the last textual touch, not necessarily the author of the behavior.
- Formatting and mechanical rewrites can replace the useful blame commit with a low-signal one; `git blame -w` ignores whitespace-only changes.
- A commit message records an author's statement, not proof the change achieved it.
- Renames, squashes, shallow clones, and rewritten history can make provenance incomplete.
- Uncommitted or staged changes sit outside commit history. `source_history` covers them from the host ledger, with each change classified by actor (you, agent, user, external, mixed). An absent ledger record is unknown provenance, not proof of no change.
- A later caller, configuration, or guard may have made old code irrelevant.

When attribution is uncertain, report the competing commits or missing history instead of choosing one by narrative fit.
