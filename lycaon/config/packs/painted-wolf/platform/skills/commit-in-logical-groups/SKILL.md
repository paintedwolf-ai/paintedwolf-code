---
name: commit-in-logical-groups
description: When the user asks to commit, split the working tree into coherent Git commits by explicit path and leave other people's uncommitted work alone.
optional_tools:
  - update_progress
---

# Commit in logical groups

**Entry check:** the user asked for the work to be committed. Committing is never an implicit part of finishing a change.

## 1. Survey once

Run one `command` program for the inventory; do not read every hunk:

```bash
git status --short
git diff --stat; git diff --cached --stat
git status --short | awk '{print $NF}' | cut -d/ -f1-2 | sort | uniq -c | sort -rn
git log --oneline -n 10
```

The area counts are the first draft of the groups; the log shows the message style. Note which paths this session wrote. The rest is the user's finished work or someone else's live work. When the user says the tree holds nobody else's live work, that settles ownership for every path.

## 2. Declare groups before reading hunks

Write the groups in `update_progress`, one line each: the reason and the explicit paths. Then read hunks (`git diff -- <path>`) only for a file whose group is ambiguous from its path and size. Budget one batch of hunk reads for the whole tree, none when the user said the tree is finished work. A hunk read may move a file between groups; it never starts a second survey.

- One commit is one reason: a feature with its tests and docs, a fix with its regression, a rename across every file it touched. Generated output travels with its source.
- A file carrying changes for two groups goes whole into the group that needs it first; the message says so. Do not split files by hunk.
- Order groups so each commit leaves the project building.
- Leave live work in a later commit, even your own.
- Leave work you did not write and the user has not claimed: do not revert, stash, or finish it.

## 3. Commit by explicit path

Each group is `git add` of its new files plus `git commit` limited to its paths, so unrelated staged work stays staged:

```bash
git add -- src/billing/refund.go src/billing/refund_test.go
git commit -m "Refund partial captures" -- src/billing/refund.go src/billing/refund_test.go
```

Put every declared group in one program, in group order, with `&&` between groups. Never stage by wildcard, `git add -A`, `git add .`, or a directory sweep. Each commit is reviewed by the host; a refused or failed commit leaves earlier commits in place. Correct and retry only the failed group, and do not replay successful commits.

Use `git commit --amend -m "<message>" -- <paths>` only for this session's own latest, unpushed commit, after `git log -n 1` shows it is still HEAD. If another commit intervened, make a follow-up commit. Rewriting older, published, or someone else's commits needs the user's explicit instruction.

## 4. Verify and finish

Run `git status --short` and `git log --oneline -n <groups>` once. If status fails, report cleanliness as unverified. Report later arrivals as remaining work; do not reopen the survey without a new request.

## Do not

- Commit work you cannot describe in one line from its paths and stat.
- Run a repository-wide `git stash`; it empties every concurrent worker's tree.
- Amend, rebase, reset, or rewrite history beyond this session's own commits.
- Bypass hooks or signing (`--no-verify`, `-c commit.gpgsign=false`). A failing hook or signing refusal is a finding to report.
- Stage secrets, credentials, local configuration, or scratch files. Read content when the path alone leaves doubt.

## Report

The commits in order with their paths; what you left uncommitted and whose it is; anything you could not group confidently; and the tree state you hand back.
