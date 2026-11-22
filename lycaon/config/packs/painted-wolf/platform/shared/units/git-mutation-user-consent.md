---
description: >-
  Git operations: reading status and diffs, comparing branches, committing,
  restoring, checking out, merging, or stashing, and the consent each one
  needs.
slot: conduct
order: 50
attaches: [command]
hosts: [coordinator]
---
{% if can_git_commit %}**Git mutations — user request only.**

- Commit, restore, switch branches, merge, stash, or drop a stash only when the user requested that effect.
{% elif profile_command_mutation != false %}**Git commits are not enabled on this surface.** Do not stage or commit directly; apply only explicitly requested repository operations that this surface admits.

{% else %}**Git is read-only on this surface.** Staging, commits, restores, checkouts, merges and stashes are refused.

{% endif %}{% if can_git_read %}- Read status, diffs and history with `git status`, `git diff` and `git log` in `command`. A dirty tree is normal; inspect the relevant paths without cleaning it. The turn-start brief already lists what other actors changed.{% if profile_has_source_history or profile_can_request.source_history %} `source_history()` answers what changed this session and by whom; `git` answers against commits.{% endif %}
{% elif can_any_git_read %}- Pass `cwd="@<label>"` in `command` to read `git status`, `git diff` and `git log` in an attached Git folder.{% if profile_has_source_history or profile_can_request.source_history %} `source_history()` answers what changed this session and by whom.{% endif %}
{% endif %}- Native Git commands retain Git authority, review, and session attribution. Unsupported native forms refuse with help{% if can_command_external_selector %}; `external -- git …` explicitly requests the confined installed Git{% endif %}.
{% if can_git_commit %}- **Dirty ≠ yours.** Uncommitted files on the primary tree may be another session's in-progress work. Do not `git restore` paths you did not write this session.
- Stage and commit explicit paths through `git add -- PATHS` and `git commit -m MESSAGE -- PATHS`. Group related writes separately. Include other work only when the user asked; preserve unrelated staging.
- **`git restore` permanently discards uncommitted edits** on the paths you name. Survey and synthesis do not require a clean tree — ground claims in tool receipts, not HEAD.
{% endif %}{% if can_git_read %}- **Compare branches directly.** In `command`, use `git rev-list --count base..head` and `git rev-list --count head..base` for ahead/behind counts, `git log base..head` for unique commits, and `git diff base head -- paths` for committed differences.
{% endif %}{% if can_git_commit %}- **Inspect operation results.** Checkout, merge and stash return observed before/after state and explicit failures or conflicts. Continue an active merge only after resolving its reported paths; abort only on request. Save only explicit stash paths, apply without deleting the recovery object, and drop it only after restoration is verified and removal requested. A denied operation remains denied through every interface.
{% endif %}