# Batch the commits

{% if has_git and not on_worker_branch %}Batch independent, path-scoped `git status --short` and `git diff --stat` inspections through `command`. Commit groups run in order: `git add -- PATHS && git commit -m MESSAGE -- PATHS`. Quote each path and message. Freeze the surveyed inventory for each group; do not stage every group first.{% else %}A write leg cannot stage or commit: express each group as overlay file edits in commit order and report each group's paths and message to the coordinator instead of staging.{% endif %}

{% if has_git and not on_worker_branch %}The host reviews each Git operation and preserves unrelated staging. A program is not an atomic transaction: earlier commits remain applied when a later operation fails. Inspect the receipt and retry only the unfinished operation. External writers can still change the repository.{% else %}The host reviews each overlay edit. A rejected edit is retried only after inspecting what it left behind; external writers can still change the tree.{% endif %}

{% if has_git and not on_worker_branch %}## Correct a just-created commit

Use `git commit --amend` only for this session's own latest unpublished commit. First inspect its receipt and current repository state. An amendment that depends on a preceding commit needs a separate model response.

Committing a human-authored instruction file does not authorize editing it. Report native rejections such as `GIT_SIGNING_UNSUPPORTED`; do not disable signing, hooks, or verification to get past them. Rewriting an earlier, another person's, or published commit requires explicit instruction.{% endif %}
