-- name: GetSessionWorktree :one
SELECT s.session_id, s.project_id, w.id AS worktree_id, w.repo_id, w.toplevel,
 w.worktree_path, w.branch, w.base_branch, w.state, w.kind, w.available, w.managed,
 w.common_dir, w.admin_dir, w.common_identity, w.admin_identity, w.worktree_identity,
 w.head_sha, w.detached, w.locked, w.lock_reason, w.prunable, w.prune_reason,
 s.created_at
FROM session_worktrees s JOIN source_worktrees w ON w.project_id=s.project_id AND w.id=s.worktree_id
WHERE s.session_id=?;

-- name: GetSourceWorktreeByID :one
SELECT id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at,
 state, kind, available, managed, common_dir, admin_dir, common_identity, admin_identity,
 worktree_identity, head_sha, detached, locked, lock_reason, prunable, prune_reason,
 updated_at, retired_at, superseded_by_id
FROM source_worktrees
WHERE project_id=? AND id=?;

-- name: GetActiveSourceWorktreeByPath :one
SELECT id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at,
 state, kind, available, managed, common_dir, admin_dir, common_identity, admin_identity,
 worktree_identity, head_sha, detached, locked, lock_reason, prunable, prune_reason,
 updated_at, retired_at, superseded_by_id
FROM source_worktrees
WHERE project_id=? AND repo_id=? AND worktree_path=? AND state='active';

-- name: ListActiveSourceWorktreesByRepo :many
SELECT id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at,
 state, kind, available, managed, common_dir, admin_dir, common_identity, admin_identity,
 worktree_identity, head_sha, detached, locked, lock_reason, prunable, prune_reason,
 updated_at, retired_at, superseded_by_id
FROM source_worktrees
WHERE project_id=? AND repo_id=? AND state='active'
ORDER BY created_at, id;

-- name: ListSourceWorktreesByRepo :many
SELECT id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at,
 state, kind, available, managed, common_dir, admin_dir, common_identity, admin_identity,
 worktree_identity, head_sha, detached, locked, lock_reason, prunable, prune_reason,
 updated_at, retired_at, superseded_by_id
FROM source_worktrees
WHERE project_id=? AND repo_id=?
ORDER BY created_at, id;

-- name: ListSupersededSourceWorktrees :many
SELECT id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at,
 state, kind, available, managed, common_dir, admin_dir, common_identity, admin_identity,
 worktree_identity, head_sha, detached, locked, lock_reason, prunable, prune_reason,
 updated_at, retired_at, superseded_by_id
FROM source_worktrees
WHERE project_id=? AND repo_id=? AND state='retired' AND superseded_by_id=?
ORDER BY retired_at DESC, id;

-- name: InsertSourceWorktree :exec
INSERT INTO source_worktrees (
 id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at,
 state, kind, available, managed, common_dir, admin_dir, common_identity, admin_identity,
 worktree_identity, head_sha, detached, locked, lock_reason, prunable, prune_reason,
 updated_at, retired_at, superseded_by_id
) VALUES (
 ?, ?, ?, ?, ?, ?, ?, ?,
 ?, ?, ?, ?, ?, ?, ?, ?,
 ?, ?, ?, ?, ?, ?, ?,
 ?, ?, ?
);

-- name: UpdateSourceWorktreeLive :exec
UPDATE source_worktrees
SET toplevel=?, worktree_path=?, branch=?, kind=?, available=?, managed=?,
 common_dir=?, admin_dir=?, common_identity=?, admin_identity=?, worktree_identity=?,
 head_sha=?, detached=?, locked=?, lock_reason=?, prunable=?, prune_reason=?,
 updated_at=?
WHERE project_id=? AND id=? AND state='active';

-- name: RetireSourceWorktree :exec
UPDATE source_worktrees
SET state='retired', available=0, retired_at=?, updated_at=?, superseded_by_id=?
WHERE project_id=? AND id=? AND state='active';

-- name: UpdateSourceWorktreeBindingMeta :exec
UPDATE source_worktrees
SET repo_id=?, toplevel=?, worktree_path=?, branch=?, base_branch=?, managed=1,
 common_dir=?, admin_dir=?, common_identity=?, admin_identity=?, worktree_identity=?,
 updated_at=?
WHERE project_id=? AND id=? AND state='active';

-- name: UpsertSessionWorktree :exec
INSERT INTO session_worktrees (session_id, project_id, worktree_id, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET project_id=excluded.project_id, worktree_id=excluded.worktree_id;

-- name: DeleteSessionWorktree :exec
DELETE FROM session_worktrees WHERE session_id=?;

-- name: CountSessionWorktreesByWorktreeID :one
SELECT COUNT(1) FROM session_worktrees WHERE project_id=? AND worktree_id=?;
