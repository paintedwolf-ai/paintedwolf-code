-- name: GetSessionWorktree :one
SELECT s.session_id, s.project_id, w.id AS worktree_id, w.repo_id, w.toplevel,
 w.worktree_path, w.branch, w.base_branch, s.created_at
FROM session_worktrees s JOIN source_worktrees w ON w.id=s.worktree_id
WHERE s.session_id=?;

-- name: UpsertSourceWorktree :one
INSERT INTO source_worktrees (id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, worktree_path) DO UPDATE SET
 repo_id=excluded.repo_id, toplevel=excluded.toplevel, branch=excluded.branch, base_branch=excluded.base_branch
RETURNING id;

-- name: UpsertSessionWorktree :exec
INSERT INTO session_worktrees (session_id, project_id, worktree_id, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET project_id=excluded.project_id, worktree_id=excluded.worktree_id;

-- name: DeleteSessionWorktree :exec
DELETE FROM session_worktrees WHERE session_id=?;
