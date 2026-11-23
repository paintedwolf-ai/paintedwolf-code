CREATE TABLE IF NOT EXISTS source_worktree_usages (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    root_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    actor_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    worker_id TEXT NOT NULL DEFAULT '',
    created_by_actor INTEGER NOT NULL DEFAULT 0 CHECK (created_by_actor IN (0, 1)),
    used_by_actor INTEGER NOT NULL DEFAULT 0 CHECK (used_by_actor IN (0, 1)),
    first_activity_id TEXT NOT NULL DEFAULT '',
    last_activity_id TEXT NOT NULL DEFAULT '',
    first_ts TEXT NOT NULL,
    last_ts TEXT NOT NULL,
    PRIMARY KEY (root_session_id, actor_session_id, worker_id, worktree_id),
    FOREIGN KEY (project_id, worktree_id) REFERENCES source_worktrees(project_id, id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_worktree_usages_project_repo
    ON source_worktree_usages(project_id, repo_id);
CREATE INDEX IF NOT EXISTS idx_source_worktree_usages_actor
    ON source_worktree_usages(actor_session_id);

CREATE TABLE IF NOT EXISTS source_worktree_usage_receipts (
    root_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    activity_id TEXT NOT NULL,
    worktree_id TEXT NOT NULL,
    relation TEXT NOT NULL CHECK (relation IN ('created', 'used')),
    actor_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    worker_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL,
    recorded_ts TEXT NOT NULL,
    PRIMARY KEY (root_session_id, activity_id, worktree_id, relation),
    FOREIGN KEY (project_id, worktree_id) REFERENCES source_worktrees(project_id, id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_worktree_usage_receipts_project
    ON source_worktree_usage_receipts(project_id, worktree_id);
