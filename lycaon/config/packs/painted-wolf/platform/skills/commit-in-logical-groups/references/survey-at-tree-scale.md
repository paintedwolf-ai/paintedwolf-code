# Survey at the scale of the tree

Read repository state with native Git commands in `command`; use `git_commit` for staging and commit review.

The inventory is `git status --short`, `git diff --stat`, and a bounded `git log`, never every hunk. Narrow large trees by explicit paths. Paths, insertion and deletion counts, and recent messages establish the initial commit groups without reading every changed line.

Groups are decided from paths, stat counts, and what recent commits were about. Write the groups down first, in `update_progress`, one line each, before any hunk is read. (If intermediate working notes are needed, store them under `@scratch/`, never in the repository.) Then read hunks (`git diff -- paths` in `command`) only for a file whose group is still ambiguous from its path and size: a new file in an unfamiliar package, a change that could belong to two features, a large diff in a file several groups touch. A hunk read may move a file between the declared groups; it never opens a second survey pass. Budget one batch of hunk reads for the whole tree, and none when the user has said the tree is finished work. On a tree of hundreds of files, reading every diff is not diligence; it is the survey never ending. Survey once, then commit.

A file that carries changes for two groups goes whole into the group that needs it first, and the message says so. Do not split a file by hunk; a commit is a set of whole files that leaves the project building.

When the user states that the tree holds no live work of anyone else's, that settles ownership for every path. Do not re-derive it file by file.
