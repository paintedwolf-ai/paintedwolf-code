---
name: commit-in-logical-groups
description: When the user asks to commit, split the working tree into coherent Git commits by explicit path and leave other people's uncommitted work alone.
optional_tools:
  - command
  - update_progress
  - git_commit
---

# Commit in logical groups

**Entry check:** the user asked for the work to be committed. Committing is never an implicit part of finishing a change.

Use the native Git tools; load any missing `git_diff`, `git_log`, or `git_commit` schema with `request_tools` before the survey. `git_commit` owns staging and review, so do not run `git add` or `git commit` through `command`.

## Mechanics

- [Survey at the scale of the tree](references/survey-at-tree-scale.md): open before the inventory on any tree larger than a few dozen files. Covers `git_status` `group_depth`, paging, `stat` diffs, and the single hunk-read batch.
- [Batch and amend](references/batch-and-amend.md): open before issuing the commits. Covers one response of `git_commit` calls, retrying only a rejected call, and amending this session's own latest commit.

## Workflow

1. **Survey once, before staging anything.** The inventory is `git_status` (with `group_depth` 2–3 on a large tree) plus `git_diff` `stat: true` and `git_log` for message style, issued together; never a read of every hunk. Write the groups in `update_progress`, one line each, before reading any hunk, then read hunks in one batch only for files whose group is ambiguous. Note which paths this session wrote; the rest is the user's finished work or someone else's live work in a shared tree.
2. **Leave what is not yours.** Work you did not write and the user has not claimed is omitted from the commit: not reverted, not stashed, not finished on their behalf. A repository-wide stash is never the way to get a clean view — it empties every concurrent worker's tree at once.
3. **Leave live work alone even when it is yours.** A file still being changed belongs in a later commit. Half a change under a confident message is worse than no commit.
4. **Group by the change, not by the directory.** One commit is one reason: a feature with its tests and docs, a fix with its regression, a rename across every file it touched. Generated output travels with the source that produced it. State each group in one line; if you cannot, it is more than one commit. A group that needs a hunk to describe gets that hunk read, not the whole tree.
5. **Order them so each stands alone.** Prefer a sequence where every commit leaves the project building — a refactor before the feature that needs it, not after.
6. **Stage by explicit path, every time.** Never stage by wildcard, "all changes", or directory sweep; a large group is still an explicit list. Issue the declared groups together: several `git_commit` calls in one response, in group order, each with its own paths and message.
7. **Write messages in the project's existing style.** State what changed and why; do not narrate the process or add trailers the project does not use.
8. **Verify once and finish.** Commit receipts confirm hashes and committed paths; omitted `uncommitted_*` fields do not prove a clean tree. Run one final `git_status`; if it fails, report cleanliness as unverified and do not infer the cause. Once the declared groups are committed, report that status and stop. List later arrivals as remaining work; do not reopen the survey without a new user request.

## Boundaries

- Do not commit work you cannot describe in one line from its paths and stat.
- Do not read on to be certain once the groups are declared. When the host reports a run of read-only batches, the next call stages a group.
- Do not amend, rebase, reset, or rewrite history beyond your own commits from this session.
- Do not bypass hooks, verification, or signing to make a commit succeed. A failing hook or a rejection such as `GIT_SIGNING_UNSUPPORTED` is a finding to report.
- Do not stage secrets, credentials, local configuration, or scratch files. Path and stat identify these; read a file's content when the path alone leaves doubt.

## Report

The commits made, in order, with the paths in each; what you deliberately left uncommitted and whose it is; anything you could not group confidently; and the state of the tree you are handing back.
