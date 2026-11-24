ALTER TABLE source_worktrees_rebuild RENAME TO source_worktrees;

CREATE UNIQUE INDEX IF NOT EXISTS idx_source_worktrees_live_path
    ON source_worktrees(project_id, repo_id, worktree_path) WHERE state = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_worktrees_live_admin
    ON source_worktrees(project_id, repo_id, common_dir, admin_dir)
    WHERE state = 'active' AND common_dir != '' AND admin_dir != '';
CREATE INDEX IF NOT EXISTS idx_source_worktrees_project_repo
    ON source_worktrees(project_id, repo_id, state);
