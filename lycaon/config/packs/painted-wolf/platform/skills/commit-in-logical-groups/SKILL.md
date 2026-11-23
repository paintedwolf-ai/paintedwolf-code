---
name: commit-in-logical-groups
description: When the user asks to commit, split the working tree into coherent Git commits by explicit path and leave other people's uncommitted work alone.
---

# Commit in logical groups

**Entry check:** the user asked for the work to be committed; committing is never implicit.

{% if can_git_commit %}Use native Git commands in `command` for inspection, staging, and commits;{% elif can_request_git_commit %}Load `command` via `request_tools(need: "stage and commit git changes")`, then use native Git commands for inspection, staging, and commits;{% else %}Prepare and report the planned commit groups for the coordinator to commit;{% endif %} each operation retains host review and attribution.

## Mechanics

- [Survey at the scale of the tree](references/survey-at-tree-scale.md): open before the inventory on trees larger than a few dozen files.
- [Batch and amend](references/batch-and-amend.md): open before issuing the commits.

## Workflow

1. **Survey once, before staging anything.**{% if can_git_read %} The inventory is `git status --short`, `git diff --stat`, and bounded `git log` in `command`.{% else %} The inventory is this worker's own change record — the files it wrote or edited.{% endif %} Write the groups, one line each{% if profile_has_update_progress %} in `update_progress`{% endif %}, before reading any hunk, then read hunks in one batch, only where a group is ambiguous.
2. **Leave what is not yours.** Work you did not write and the user has not claimed is left out of your groups: not reverted, not stashed, not finished for them. A repository-wide stash empties every concurrent worker's tree; never use one for a clean view.
3. **Leave live work alone even when it is yours.** A file still being changed belongs in a later commit.
4. **Group by the change, not by the directory.** One commit is one reason: a feature with its tests, a fix with its regression, a rename across every file it touched. Generated output travels with its source. State each group in one line; if you cannot, it is more than one commit. A group that needs a hunk to describe gets that hunk read, not the tree.
5. **Order them so each stands alone.** Prefer a sequence where every commit leaves the project building — a refactor before its feature.
6. **Carry each group by explicit path.**{% if can_git_commit %} Never stage by wildcard, "all changes", or directory sweep; a large group is still an explicit list. For each group, run `git add -- PATHS` followed by `git commit -m MESSAGE -- PATHS` in order, proceeding only after each success.{% else %} Express each group by explicit paths in commit order (never wildcard or directory sweeps) and report its paths and message to the coordinator.{% endif %}
7. **Write messages in the project's existing style.** State what changed and why; do not narrate the process or add unused trailers.
8. {% if can_git_commit %}**Verify once and finish.** Commit receipts confirm hashes and paths; omitted `uncommitted_*` fields do not prove a clean tree. Run one final `git status --short` in `command`; if it fails, report cleanliness as unverified. Once the declared groups are committed, report and stop.{% else %}**Report the plan and stop.** {% if can_git_read %}Inspect `git status --short` in `command` for current tree state, and hand{% else %}Hand{% endif %} the coordinator the planned groups, paths, messages, and anything ambiguous{% if not can_git_read %} (do not claim a clean tree without Git){% endif %}.{% endif %} List later arrivals as remaining work; do not reopen the survey without a new request.

## Boundaries

- Do not commit work you cannot describe in one line from its paths and stat, or read on once groups are declared.
- Do not amend, rebase, reset, or rewrite history beyond your own commits from this session.
- Do not bypass hooks, verification, or signing (`GIT_SIGNING_UNSUPPORTED` is a finding to report).
- Do not carry secrets, credentials, local config, or scratch files; read content when a path leaves doubt.

## Report

{% if can_git_commit %}The commits made{% else %}The groups planned{% endif %} in order, with each one's paths; the paths you left out and whose they are; anything you could not group confidently; and the tree state you hand back.
