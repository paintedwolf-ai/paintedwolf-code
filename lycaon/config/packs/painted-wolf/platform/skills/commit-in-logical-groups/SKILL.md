---
name: commit-in-logical-groups
description: When the user asks to commit, split the working tree into coherent Git commits by explicit path and leave other people's uncommitted work alone.
---

# Commit in logical groups

**Entry check:** the user asked for the work to be committed; committing is never implicit.

Use native Git commands through `command` for inspection, staging, and commits; on a write leg, prepare the groups as overlay file edits and report the planned grouping to the coordinator. Each operation retains host review and session attribution.

## Mechanics

- [Survey at the scale of the tree](references/survey-at-tree-scale.md): open before the inventory on trees larger than a few dozen files.
- [Batch and amend](references/batch-and-amend.md): open before issuing the commits.

## Workflow

1. **Survey once, before staging anything.**{% if has_git and not on_worker_branch %} The inventory is `git status --short`, `git diff --stat`, and bounded `git log` in `command`.{% else %} The inventory is this leg's own change record — the files it wrote or edited.{% endif %} Write the groups, one line each{% if profile_has_update_progress %} in `update_progress`{% endif %}, before reading any hunk, then read hunks in one batch, only where a group is ambiguous.
2. **Leave what is not yours.** Work you did not write and the user has not claimed is left out of your groups: not reverted, not stashed, not finished for them. A repository-wide stash empties every concurrent worker's tree; never use one for a clean view.
3. **Leave live work alone even when it is yours.** A file still being changed belongs in a later commit.
4. **Group by the change, not by the directory.** One commit is one reason: a feature with its tests, a fix with its regression, a rename across every file it touched. Generated output travels with its source. State each group in one line; if you cannot, it is more than one commit. A group that needs a hunk to describe gets that hunk read, not the tree.
5. **Order them so each stands alone.** Prefer a sequence where every commit leaves the project building — a refactor before its feature.
6. **Carry each group by explicit path.**{% if has_git and not on_worker_branch %} Never stage by wildcard, "all changes", or directory sweep; a large group is still an explicit list. For each group, run `git add -- PATHS` followed by `git commit -m MESSAGE -- PATHS`. Preserve group order; proceed only after each success.{% else %} Express each group as its overlay file edits, in commit order, and report its paths and message to the coordinator; never sweep by wildcard or directory.{% endif %}
7. **Write messages in the project's existing style.** State what changed and why; do not narrate the process or add unused trailers.
8. {% if has_git and not on_worker_branch %}**Verify once and finish.** Commit receipts confirm hashes and paths; omitted `uncommitted_*` fields do not prove a clean tree. Run one final `git status --short` in `command`; if it fails, report cleanliness as unverified. Once the declared groups are committed, report and stop.{% else %}**Report the plan and stop.** Hand the coordinator the planned groups with their paths and messages and anything you could not group confidently; do not report the tree clean — only Git can verify that.{% endif %} List later arrivals as remaining work; do not reopen the survey without a new request.

## Boundaries

- Do not commit work you cannot describe in one line from its paths and stat.
- Do not read on to be certain once the groups are declared.
- Do not amend, rebase, reset, or rewrite history beyond your own commits from this session.
- Do not bypass hooks, verification, or signing to make a commit succeed; a rejection such as `GIT_SIGNING_UNSUPPORTED` is a finding to report.
- Do not carry secrets, credentials, local configuration, or scratch files; read a file's content when its path alone leaves doubt.

## Report

The commits made — or, without Git, the groups planned — in order, with each one's paths; the paths you left out and whose they are; anything you could not group confidently; and the tree state you hand back.
