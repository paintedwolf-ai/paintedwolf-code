-- Human-in-the-loop approval checkpoints.

-- name: InsertCheckpoint :exec
INSERT INTO checkpoints (
    id, session_id, project_dir, kind, status, type, title, description,
    tool_name, path, args_json, files_json, payload_json, result_json, created_at, resolved_at, resolved_by, resolved_by_person_id,
    project_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetCheckpoint :one
SELECT id, session_id, project_dir, kind, status, type, title, description,
       tool_name, path, args_json, files_json, payload_json, result_json, created_at, resolved_at, resolved_by, resolved_by_person_id,
       project_id
FROM checkpoints
WHERE id = ?;

-- ListSessionCheckpoints applies the status and kind filters only when supplied,
-- so one prepared statement covers every caller combination.
-- name: ListSessionCheckpoints :many
SELECT id, session_id, project_dir, kind, status, type, title, description,
       tool_name, path, args_json, files_json, payload_json, result_json, created_at, resolved_at, resolved_by, resolved_by_person_id,
       project_id
FROM checkpoints
WHERE session_id = sqlc.arg(session_id)
  AND (sqlc.narg(status) IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(kind) IS NULL OR kind = sqlc.narg(kind))
ORDER BY created_at ASC, id ASC;

-- name: ListPendingCheckpoints :many
SELECT id, session_id, project_dir, kind, status, type, title, description,
       tool_name, path, args_json, files_json, payload_json, result_json, created_at, resolved_at, resolved_by, resolved_by_person_id,
       project_id
FROM checkpoints
WHERE status = 'pending'
ORDER BY created_at ASC, id ASC;

-- name: ListPendingCheckpointSessions :many
SELECT session_id, CAST(MIN(created_at) AS TEXT) AS oldest_created_at
FROM pending_checkpoint_scopes
GROUP BY session_id;

-- A denial coalesces repeated asks only until the chat's next visible user
-- intent, so restart restores the denials resolved after that boundary. The
-- boundary predicate is idx_messages_user_turn; timestamps use the fixed-width
-- UTC layout, so text order is time order.
-- name: ListRejectedToolApprovalCheckpoints :many
SELECT c.id, c.session_id, c.project_dir, c.kind, c.status, c.type, c.title, c.description,
       c.tool_name, c.path, c.args_json, c.files_json, c.payload_json, c.result_json, c.created_at, c.resolved_at, c.resolved_by, c.resolved_by_person_id,
       c.project_id
FROM checkpoints c
WHERE c.kind = 'tool_approval' AND c.status = 'rejected'
  AND c.resolved_at > COALESCE((
    SELECT m.ts FROM messages m
    WHERE m.session_id = c.session_id
      AND m.role = 'user'
      AND m.visibility <> 'internal'
      AND m.kind <> 'workflow_boundary'
      AND m.kind <> 'user_continuation'
      AND m.workflow_boundary_json IS NULL
    ORDER BY m.ord DESC
    LIMIT 1), '')
ORDER BY c.resolved_at ASC, c.id ASC;

-- name: UpdatePendingCheckpointPayload :execrows
UPDATE checkpoints SET payload_json = ? WHERE id = ? AND status = ?;

-- name: LatestResolvedCheckpointStatus :one
SELECT c.status FROM checkpoints c
JOIN checkpoint_resolution_ordinals resolved ON resolved.checkpoint_id = c.id
WHERE c.session_id = ? AND c.kind = ? AND c.status != ?
ORDER BY resolved.ordinal DESC
LIMIT 1;

-- name: ResolveCheckpointForSession :execrows
UPDATE checkpoints
SET status = sqlc.arg(new_status), result_json = sqlc.arg(result_json),
    resolved_at = sqlc.arg(resolved_at), resolved_by = sqlc.arg(resolved_by),
    resolved_by_person_id = sqlc.narg(resolved_by_person_id)
WHERE id = sqlc.arg(id) AND session_id = sqlc.arg(session_id)
  AND status = sqlc.arg(expected_status);

-- name: PrepareApprovalOperation :execrows
INSERT INTO approval_operations (
    checkpoint_id, session_id, option_id, option_json, status, created_at
) VALUES (?, ?, ?, ?, 'prepared', ?)
ON CONFLICT(checkpoint_id) DO UPDATE SET
    session_id = excluded.session_id,
    option_id = excluded.option_id,
    option_json = excluded.option_json,
    status = 'prepared',
    created_at = excluded.created_at,
    committed_at = NULL,
    rolled_back_at = NULL
WHERE approval_operations.status = 'rolled_back';

-- name: CommitApprovalCheckpoint :execrows
UPDATE checkpoints
SET status = 'approved', result_json = sqlc.arg(result_json),
    resolved_at = sqlc.arg(resolved_at), resolved_by = sqlc.arg(resolved_by),
    resolved_by_person_id = sqlc.narg(resolved_by_person_id)
WHERE id = sqlc.arg(id) AND session_id = sqlc.arg(session_id)
  AND status = 'pending';

-- name: CommitApprovalOperation :execrows
UPDATE approval_operations
SET status = 'committed', committed_at = sqlc.arg(committed_at)
WHERE checkpoint_id = sqlc.arg(checkpoint_id) AND status = 'prepared';

-- name: RollbackApprovalOperation :exec
UPDATE approval_operations
SET status = 'rolled_back', rolled_back_at = sqlc.arg(rolled_back_at)
WHERE checkpoint_id = sqlc.arg(checkpoint_id) AND status = 'prepared';

-- name: ListPreparedApprovalOperations :many
SELECT checkpoint_id, session_id, option_json
FROM approval_operations
WHERE status = 'prepared'
ORDER BY created_at, checkpoint_id;

-- name: UpsertCheckpointDecisionStamp :exec
INSERT INTO checkpoint_decision_stamps (
    session_id, tool_call_id, decision_json, created_at
) VALUES (?, ?, ?, ?)
ON CONFLICT(session_id, tool_call_id) DO UPDATE SET
    decision_json = excluded.decision_json,
    created_at = excluded.created_at;

-- name: DeleteCheckpointDecisionStamp :exec
DELETE FROM checkpoint_decision_stamps
WHERE session_id = ? AND tool_call_id = ?;

-- name: ListSessionToolResults :many
SELECT id, tool_result_json
FROM messages
WHERE session_id = ? AND role = 'tool' AND tool_result_json IS NOT NULL
ORDER BY ord, ts;

-- name: UpdateMessageToolResult :exec
UPDATE messages
SET tool_result_json = sqlc.arg(tool_result_json)
WHERE id = sqlc.arg(id) AND session_id = sqlc.arg(session_id);

-- name: GetCheckpointDecisionStamp :one
SELECT decision_json FROM checkpoint_decision_stamps
WHERE session_id = ? AND tool_call_id = ?;

-- name: ListParentPendingCheckpoints :many
SELECT c.id, c.session_id, c.project_dir, c.kind, c.status, c.type, c.title, c.description,
       c.tool_name, c.path, c.args_json, c.files_json, c.payload_json, c.result_json, c.created_at, c.resolved_at, c.resolved_by, c.resolved_by_person_id,
       c.project_id
FROM checkpoints c
JOIN pending_checkpoint_scopes scope ON scope.checkpoint_id = c.id
WHERE scope.session_id = sqlc.arg(parent_session_id)
  AND (sqlc.narg(kind) IS NULL OR c.kind = sqlc.narg(kind))
ORDER BY c.created_at, c.id;

-- name: UpsertChatGrant :exec
INSERT INTO chat_grants (
    chat_session_id, id, checkpoint_id, delta_json, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_session_id, id) DO UPDATE SET
    checkpoint_id = excluded.checkpoint_id,
    delta_json = excluded.delta_json,
    created_at = excluded.created_at,
    expires_at = excluded.expires_at;

-- name: ListChatGrants :many
SELECT chat_session_id, id, checkpoint_id, delta_json, expires_at
FROM chat_grants
ORDER BY created_at, chat_session_id, id;

-- name: DeleteChatGrant :execrows
DELETE FROM chat_grants WHERE id = sqlc.arg(id);

-- name: DeleteExpiredChatGrants :exec
DELETE FROM chat_grants
WHERE expires_at IS NOT NULL AND expires_at <= sqlc.arg(now);
