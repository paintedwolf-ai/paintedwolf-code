# Survey at the scale of the tree

Use native Git tools (`git_status`, `git_diff`, `git_log`, `git_show`, `git_commit`) exclusively; invoking `git` through `command` is refused.

The inventory is `git_status` plus `git_diff` with `stat: true`, never a read of every hunk. `git_status` returns pages of 80 files. On large trees, start with `git_status` and `group_depth` (2 or 3): it returns `groups`, showing which areas changed and how much, largest first, before any file is listed. Use `groups` as the first draft of the commit groups. Then page files: follow `next_offset` until absent, or pass `paths: ["<prefix>/"]` to narrow inspection to target areas so file lists fit comfortably within the 80-file window. `git_diff` with `stat: true` gives per-file insertion and deletion counts for an area. With `git_log` for recent commit conventions, that completes the survey in a handful of calls.

Groups are decided from paths, stat counts, and what recent commits were about. Write the groups down first, in `update_progress`, one line each, before any hunk is read. (If intermediate working notes are needed, store them under `@scratch/`, never in the repository.) Then read hunks (`git_diff` on named paths) only for a file whose group is still ambiguous from its path and size: a new file in an unfamiliar package, a change that could belong to two features, a large diff in a file several groups touch. A hunk read may move a file between the declared groups; it never opens a second survey pass. Budget one batch of hunk reads for the whole tree, and none when the user has said the tree is finished work. On a tree of hundreds of files, reading every diff is not diligence; it is the survey never ending. Survey once, then commit.

A file that carries changes for two groups goes whole into the group that needs it first, and the message says so. Do not split a file by hunk; a commit is a set of whole files that leaves the project building.

When the user states that the tree holds no live work of anyone else's, that settles ownership for every path. Do not re-derive it file by file.
