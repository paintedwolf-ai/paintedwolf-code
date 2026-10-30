# Batch the commits

Every tool call in one response settles before the next model turn, so a sequence you can name in advance is one response, not one turn per step. Follow the returned `next_offset` for status pages; batch each known page with independent per-area `git_diff` `stat: true` calls. Declared groups become one response of `git_commit` calls in group order. A call whose arguments depend on a sibling's result needs a turn boundary, including an amendment that depends on the preceding commit's receipt.

Each `git_commit` stages its explicit paths, including new files, and immediately commits them within the same call. The host holds its repository lease across both steps. Freeze the surveyed inventory into each group's paths and message, then submit the planned calls together; do not stage every group first or insert another survey between commits. This batches the work without a model turn between a group's add and commit. The lease serializes participating host Git operations; it does not freeze external writers or make the whole sequence one atomic transaction.

Accepted calls remain complete when a sibling fails. Correct and retry only the rejected call; do not replay successful commits.

## Correct a just-created commit

Use `git_commit` for both new commits and amendments. It stages new files as well as tracked edits, including human-authored `AGENTS.md` changes the user asked to commit; committing that file does not permit editing it. Do not run `git add` or `git commit` through `command` to assemble a group or repair a missing path. A native rejection such as `GIT_SIGNING_UNSUPPORTED` is a blocker to report, not a reason to switch to `command`, change verification flags, or disable signing.

To complete the session's own latest, unpushed commit, pass `amend: true`, the final `message`, and explicit `paths` for the correction. The commit keeps its existing files, incorporates those paths, and leaves unrelated staged changes alone. Confirm from the commit receipt and current `git_status` head that the latest commit is still the one this session created. If another commit has intervened, make a follow-up commit instead. Rewriting an older, someone else's, or published commit requires the user's explicit instruction. A missing file discovered before committing belongs in the original group's path list, avoiding an amendment entirely.
