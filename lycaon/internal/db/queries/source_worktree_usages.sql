-- name: InsertSourceWorktreeUsageReceipt :execrows
INSERT OR IGNORE INTO source_worktree_usage_receipts (
    root_session_id, activity_id, worktree_id, relation,
    actor_session_id, worker_id, project_id, repo_id, recorded_ts
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: UpsertSourceWorktreeUsage :exec
INSERT INTO source_worktree_usages (
    project_id, repo_id, worktree_id, root_session_id, actor_session_id, worker_id,
    created_by_actor, used_by_actor, first_activity_id, last_activity_id, first_ts, last_ts
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (root_session_id, actor_session_id, worker_id, worktree_id) DO UPDATE SET
    created_by_actor = MAX(source_worktree_usages.created_by_actor, excluded.created_by_actor),
    used_by_actor = MAX(source_worktree_usages.used_by_actor, excluded.used_by_actor),
    last_activity_id = CASE WHEN excluded.last_activity_id != '' THEN excluded.last_activity_id ELSE source_worktree_usages.last_activity_id END,
    last_ts = CASE WHEN excluded.last_ts > source_worktree_usages.last_ts THEN excluded.last_ts ELSE source_worktree_usages.last_ts END;

-- name: ListSourceWorktreeUsagesByRootSession :many
SELECT
    project_id, repo_id, worktree_id, root_session_id, actor_session_id, worker_id,
    created_by_actor, used_by_actor, first_activity_id, last_activity_id, first_ts, last_ts
FROM source_worktree_usages
WHERE root_session_id = ?
ORDER BY last_ts DESC, worktree_id ASC;

-- name: ListSourceWorktreeUsagesByRepoAndRootSession :many
SELECT
    project_id, repo_id, worktree_id, root_session_id, actor_session_id, worker_id,
    created_by_actor, used_by_actor, first_activity_id, last_activity_id, first_ts, last_ts
FROM source_worktree_usages
WHERE project_id = ? AND repo_id = ? AND root_session_id = ?
ORDER BY last_ts DESC, worktree_id ASC;

-- name: CountSourceWorktreeUsageReceipts :one
SELECT COUNT(*)
FROM source_worktree_usage_receipts
WHERE root_session_id = ? AND worktree_id = ? AND relation = ?;
