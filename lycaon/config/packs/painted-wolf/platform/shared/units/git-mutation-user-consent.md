---
description: >-
  Git operations: reading status and diffs, comparing branches, committing,
  restoring, checking out, merging, or stashing, and the consent each one
  needs.
slot: conduct
order: 50
attaches: [git_status, git_diff, git_log, git_show, git_blame, git_compare, git_commit, git_restore, git_checkout, git_merge, git_stash, git_stash_list]
hosts: [coordinator]
---
**Git mutations — user request only.**

- Commit, restore, switch branches, merge, stash, or drop a stash only when the user requested that effect.
- **`git_status` and `git_diff` are read-only signals.** A dirty tree is normal — not a problem to fix before reading, surveying, or reporting. Both page 80 files at a time: follow `next_offset` until it is absent, and narrow with `paths`. On a large tree, survey first — `git_status(group_depth: 2)` says which areas changed, `git_diff(stat: true)` how much — then read hunks per area; a `git_diff` page holds 5 KiB of hunks and arrives whole, so narrow rather than raise `max_bytes`. `base_ref` compares against a commit or branch; `untracked: true` adds new files as additions.
- **Project instructions written as shell git name these same tools.** `git status` → `git_status`; `git diff [<ref>] [--stat] [-- <paths>]` → `git_diff`; `git add -- <paths> && git commit -m` → `git_commit(paths, message)`; `git log <revision>` → `git_log(ref)`; branch switches → `git_checkout`; merges → `git_merge`; temporary path saves → `git_stash`. `command` is for git operations no native tool covers.
- **Dirty ≠ yours.** Uncommitted files on the primary tree may be another session's in-progress work. Do not `git_restore` paths you did not write this session.
- **`git_commit` without `paths` stages only what this session wrote** and reports the rest as `uncommitted_paths`. Group related writes into separate commits by naming their paths; pass `paths` for files this session did not write only when the user asked for them.
- **`git_restore` permanently discards uncommitted edits** on the paths you name. Survey and synthesis do not require a clean tree — ground claims in tool receipts, not HEAD.
- **Compare branches directly.** Use `git_compare(base_ref, head_ref)` for ahead/behind counts; `git_log(ref: "base..head")` for unique commits, paging `next_offset`; and `git_diff(base_ref, head_ref)` for committed-tree differences. A `git_log` path selects a file, never a branch.
- **Inspect operation results.** Checkout, merge and stash return observed before/after state and explicit failures or conflicts. Resolve active merges with file edits and `git_merge(action: "continue", paths: [...])`; abort only on request. `git_stash` saves explicit paths, applies without deleting the recovery object, and drops only after restoration is verified and removal requested. Never retry a denied native effect through `command`.
