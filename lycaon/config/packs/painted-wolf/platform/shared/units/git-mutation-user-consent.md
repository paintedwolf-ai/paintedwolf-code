---
description: >-
  Git operations: reading status and diffs, comparing branches, committing,
  restoring, checking out, merging, or stashing, and the consent each one
  needs.
slot: conduct
order: 50
attaches: [command, git_commit, git_restore, git_checkout, git_merge, git_stash]
hosts: [coordinator]
---
**Git mutations — user request only.**

- Commit, restore, switch branches, merge, stash, or drop a stash only when the user requested that effect.
- Read status, diffs and history with `git status`, `git diff` and `git log` in `command`. A dirty tree is normal; inspect the relevant paths without cleaning it.
- Native Git commands run through the same Git authority as the domain tools. Unsupported native forms refuse with help; `external -- git …` explicitly requests the confined installed Git.
- **Dirty ≠ yours.** Uncommitted files on the primary tree may be another session's in-progress work. Do not `git_restore` paths you did not write this session.
- **`git_commit` without `paths` stages only what this session wrote** and reports the rest as `uncommitted_paths`. Group related writes into separate commits by naming their paths; pass `paths` for files this session did not write only when the user asked for them.
- **`git_restore` permanently discards uncommitted edits** on the paths you name. Survey and synthesis do not require a clean tree — ground claims in tool receipts, not HEAD.
- **Compare branches directly.** In `command`, use `git rev-list --left-right --count base...head` for ahead/behind counts, `git log base..head` for unique commits, and `git diff base head -- paths` for committed differences.
- **Inspect operation results.** Checkout, merge and stash return observed before/after state and explicit failures or conflicts. Continue an active merge only after resolving its reported paths; abort only on request. Save only explicit stash paths, apply without deleting the recovery object, and drop it only after restoration is verified and removal requested. A denied operation remains denied through every interface.
