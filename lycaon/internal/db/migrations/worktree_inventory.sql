CREATE TABLE source_worktrees_rebuild (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL,
    toplevel TEXT NOT NULL,
    worktree_path TEXT NOT NULL,
    branch TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    created_at TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'retired')),
    kind TEXT NOT NULL DEFAULT 'linked' CHECK (kind IN ('primary', 'linked')),
    available INTEGER NOT NULL DEFAULT 1 CHECK (available IN (0, 1)),
    managed INTEGER NOT NULL DEFAULT 0 CHECK (managed IN (0, 1)),
    common_dir TEXT NOT NULL DEFAULT '',
    admin_dir TEXT NOT NULL DEFAULT '',
    common_identity TEXT NOT NULL DEFAULT '',
    admin_identity TEXT NOT NULL DEFAULT '',
    worktree_identity TEXT NOT NULL DEFAULT '',
    head_sha TEXT NOT NULL DEFAULT '',
    detached INTEGER NOT NULL DEFAULT 0 CHECK (detached IN (0, 1)),
    locked INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0, 1)),
    lock_reason TEXT NOT NULL DEFAULT '',
    prunable INTEGER NOT NULL DEFAULT 0 CHECK (prunable IN (0, 1)),
    prune_reason TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT '',
    retired_at TEXT NOT NULL DEFAULT '',
    superseded_by_id TEXT NOT NULL DEFAULT '',
    UNIQUE(project_id, id)
) STRICT;

INSERT INTO source_worktrees_rebuild (
    id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at,
    state, kind, available, managed, updated_at
)
SELECT
    w.id, w.project_id, w.repo_id, w.toplevel, w.worktree_path, w.branch, w.base_branch, w.created_at,
    'active', 'linked', 1,
    CASE WHEN EXISTS (
        SELECT 1 FROM session_worktrees s
        WHERE s.project_id = w.project_id AND s.worktree_id = w.id
    ) THEN 1 ELSE 0 END,
    w.created_at
FROM source_worktrees w;

DROP TABLE source_worktrees;
ALTER TABLE source_worktrees_rebuild RENAME TO source_worktrees;

CREATE UNIQUE INDEX IF NOT EXISTS idx_source_worktrees_live_path
    ON source_worktrees(project_id, repo_id, worktree_path) WHERE state = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_worktrees_live_admin
    ON source_worktrees(project_id, repo_id, common_dir, admin_dir)
    WHERE state = 'active' AND common_dir != '' AND admin_dir != '';
CREATE INDEX IF NOT EXISTS idx_source_worktrees_project_repo
    ON source_worktrees(project_id, repo_id, state);
