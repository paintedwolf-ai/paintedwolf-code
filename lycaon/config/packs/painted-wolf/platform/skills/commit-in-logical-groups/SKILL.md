---
name: commit-in-logical-groups
description: When asked to commit, group changes coherently and exclude other people’s uncommitted work.
optional_tools:
  - git_status
  - git_diff
  - git_log
  - git_show
  - update_progress
  - git_commit
---

# Commit in logical groups

**Entry check:** the user asked for the work to be committed. Committing is never an implicit part of finishing a change.

If not already present in the active tool set, load any missing `git_diff`, `git_log`, and `git_commit` schemas with `request_tools` before the survey.

## Survey at the scale of the tree

Use native Git tools (`git_status`, `git_diff`, `git_log`, `git_show`, `git_commit`) exclusively; invoking `git` through `command` is refused.

The inventory is `git_status` plus `git_diff` with `stat: true`, never a read of every hunk. `git_status` returns pages of 80 files. On large trees, start with `git_status` and `group_depth` (2 or 3): it returns `groups`, showing which areas changed and how much, largest first, before any file is listed. Use `groups` as the first draft of the commit groups. Then page files: follow `next_offset` until absent, or pass `paths: ["<prefix>/"]` to narrow inspection to target areas so file lists fit comfortably within the 80-file window. `git_diff` with `stat: true` gives per-file insertion and deletion counts for an area. With `git_log` for recent commit conventions, that completes the survey in a handful of calls.

Groups are decided from paths, stat counts, and what recent commits were about. Write the groups down first, in `update_progress`, one line each, before any hunk is read. (If intermediate working notes are needed, store them under `@scratch/`, never in the repository.) Then read hunks (`git_diff` on named paths) only for a file whose group is still ambiguous from its path and size: a new file in an unfamiliar package, a change that could belong to two features, a large diff in a file several groups touch. A hunk read may move a file between the declared groups; it never opens a second survey pass. Budget one batch of hunk reads for the whole tree, and none when the user has said the tree is finished work. On a tree of hundreds of files, reading every diff is not diligence; it is the survey never ending. Survey once, then commit.

A file that carries changes for two groups goes whole into the group that needs it first, and the message says so. Do not split a file by hunk; a commit is a set of whole files that leaves the project building.

When the user states that the tree holds no live work of anyone else's, that settles ownership for every path. Do not re-derive it file by file.

## Stack the calls you already know you need

Every tool call in one response settles before the next model turn, so a sequence you can name in advance is one response, not one turn per step. Follow the returned `next_offset` for status pages; batch each known page with independent per-area `git_diff` `stat: true` calls. Declared groups become one response of `git_commit` calls in group order. A call whose arguments depend on a sibling's result needs a turn boundary, including an amendment that depends on the preceding commit's receipt.

Each `git_commit` stages its explicit paths, including new files, and immediately commits them within the same call. The host holds its repository lease across both steps. Freeze the surveyed inventory into each group's paths and message, then submit the planned calls together; do not stage every group first or insert another survey between commits. This batches the work without a model turn between a group's add and commit. The lease serializes participating host Git operations; it does not freeze external writers or make the whole sequence one atomic transaction.

Accepted calls remain complete when a sibling fails. Correct and retry only the rejected call; do not replay successful commits.

## Correct a just-created commit

Use `git_commit` for both new commits and amendments. It stages new files as well as tracked edits, including human-authored `AGENTS.md` changes the user asked to commit; committing that file does not permit editing it. Do not run `git add` or `git commit` through `command` to assemble a group or repair a missing path. On `Code: USE_NATIVE_TOOL`, use `replacement_calls` exactly. A native rejection such as `GIT_SIGNING_UNSUPPORTED` is a blocker to report, not a reason to switch to `command`, change verification flags, or disable signing.

To complete the session's own latest, unpushed commit, pass `amend: true`, the final `message`, and explicit `paths` for the correction. The commit keeps its existing files, incorporates those paths, and leaves unrelated staged changes alone. Confirm from the commit receipt and current `git_status` head that the latest commit is still the one this session created. If another commit has intervened, make a follow-up commit instead. Rewriting an older, someone else's, or published commit requires the user's explicit instruction. A missing file discovered before committing belongs in the original group's path list, avoiding an amendment entirely.

## Workflow

1. **Read the tree before staging anything.** Take the inventory as above. Note which paths this session wrote; the rest is either the user's finished work or someone else's live work. A working tree is shared — another session, another agent, or the user's editor may own changes in it.
2. **Leave what is not yours.** Work you did not write and the user has not claimed is omitted from the commit: not reverted, not stashed, not finished on their behalf. A repository-wide stash is never the way to get a clean view — it empties every concurrent worker's tree at once.
3. **Leave live work alone even when it is yours.** A file still being changed belongs in a later commit. Half a change under a confident message is worse than no commit.
4. **Group by the change, not by the directory.** One commit is one reason: a feature with its tests and docs, a fix with its regression, a rename across every file it touched. Generated output travels with the source that produced it. State each group in one line; if you cannot, it is more than one commit. A group that needs a hunk to describe gets that hunk read, not the whole tree.
5. **Order them so each stands alone.** Prefer a sequence where every commit leaves the project building — a refactor before the feature that needs it, not after.
6. **Stage by explicit path, every time.** Name the files in each group. Never stage by wildcard, by "all changes", or by directory sweep; that is how a neighbour's work ends up under your message. A large group is still an explicit list. Once the groups are declared, issue the commits together: several `git_commit` calls in one response, in group order, each with its own paths and message.
7. **Write messages in the project's existing style.** Read recent history for format, prefix, and length first. State what changed and why; do not narrate the process that produced it, and do not add trailers the project does not use.
8. **Verify once and finish.** Commit receipts confirm hashes and committed paths; omitted `uncommitted_*` fields do not prove a clean tree. Run one final `git_status`; if it fails, report cleanliness as unverified and do not infer the cause. Once the declared groups are committed, report that status and stop. List later arrivals as remaining work; do not reopen the survey without a new user request.

## Boundaries

- Do not commit work you cannot describe in one line from its paths and stat.
- Do not read on to be certain once the groups are declared. When the host reports a run of read-only batches, the next call stages a group.
- Do not amend, rebase, reset, or rewrite history beyond your own commits from this session.
- Do not bypass hooks or verification to make a commit succeed; a failing hook is a finding to report.
- Do not stage secrets, credentials, local configuration, or scratch files. Path and stat identify these; read a file's content when the path alone leaves doubt.

## Report

The commits made, in order, with the paths in each; what you deliberately left uncommitted and whose it is; anything you could not group confidently; and the state of the tree you are handing back.
