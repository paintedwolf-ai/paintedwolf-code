-- Human-in-the-loop approval checkpoints.

-- name: InsertCheckpoint :exec
INSERT INTO checkpoints (
    id, session_id, project_dir, kind, status, type, title, description,
    tool_name, path, args_json, files_json, payload_json, result_json, created_at, resolved_at, resolved_by, resolved_by_person_id,
    project_id, authority_format, authority_digest, authority_body
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetCheckpoint :one
SELECT id, session_id, project_dir, kind, status, type, title, description,
       tool_name, path, args_json, files_json, payload_json, result_json, created_at, resolved_at, resolved_by, resolved_by_person_id,
       project_id, authority_format
FROM checkpoints
WHERE id = ?;

-- ListSessionCheckpoints applies the status and kind filters only when supplied,
-- so one prepared statement covers every caller combination.
-- name: ListSessionCheckpoints :many
SELECT id, session_id, project_dir, kind, status, type, title, description,
       tool_name, path, args_json, files_json, payload_json, result_json, created_at, resolved_at, resolved_by, resolved_by_person_id,
       project_id, authority_format
FROM checkpoints
WHERE session_id = sqlc.arg(session_id)
  AND (sqlc.narg(status) IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(kind) IS NULL OR kind = sqlc.narg(kind))
ORDER BY created_at ASC, id ASC;

-- name: ListPendingCheckpoints :many
SELECT id, session_id, project_dir, kind, status, type, title, description,
       tool_name, path, args_json, files_json, payload_json, result_json, created_at, resolved_at, resolved_by, resolved_by_person_id,
       project_id, authority_format
FROM checkpoints
WHERE status = 'pending'
ORDER BY created_at ASC, id ASC;

-- name: ListPendingCheckpointSessions :many
SELECT session_id, CAST(MIN(created_at) AS TEXT) AS oldest_created_at
FROM pending_checkpoint_scopes
GROUP BY session_id;

-- name: UpdatePendingCheckpointPayload :execrows
UPDATE checkpoints SET payload_json = ? WHERE id = ? AND status = ?;

-- name: ResolveCheckpointForSession :execrows
UPDATE checkpoints
SET status = sqlc.arg(new_status), result_json = sqlc.arg(result_json),
    resolved_at = sqlc.arg(resolved_at), resolved_by = sqlc.arg(resolved_by),
    resolved_by_person_id = sqlc.narg(resolved_by_person_id)
WHERE id = sqlc.arg(id) AND session_id = sqlc.arg(session_id)
  AND status = sqlc.arg(expected_status);

-- name: PrepareApprovalOperation :execrows
INSERT INTO approval_operations (
    checkpoint_id, session_id, option_id, option_json, status, created_at, authority_format, authority_digest, authority_body
) VALUES (?, ?, ?, ?, 'prepared', ?, ?, ?, ?)
ON CONFLICT(checkpoint_id) DO UPDATE SET
    session_id = excluded.session_id,
    option_id = excluded.option_id,
    option_json = excluded.option_json,
    authority_format = excluded.authority_format,
    authority_digest = excluded.authority_digest,
    authority_body = excluded.authority_body,
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
SELECT checkpoint_id, session_id, option_json, authority_format
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
       c.project_id, c.authority_format
FROM checkpoints c
JOIN pending_checkpoint_scopes scope ON scope.checkpoint_id = c.id
WHERE scope.session_id = sqlc.arg(parent_session_id)
  AND (sqlc.narg(kind) IS NULL OR c.kind = sqlc.narg(kind))
ORDER BY c.created_at, c.id;

-- name: UpsertChatGrant :exec
INSERT INTO chat_grants (
    chat_session_id, id, checkpoint_id, delta_json, created_at, expires_at, authority_format, authority_digest, authority_body
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_session_id, id) DO UPDATE SET
    checkpoint_id = excluded.checkpoint_id,
    delta_json = excluded.delta_json,
    authority_format = excluded.authority_format,
    authority_digest = excluded.authority_digest,
    authority_body = excluded.authority_body,
    created_at = excluded.created_at,
    expires_at = excluded.expires_at;

-- name: ListChatGrants :many
SELECT chat_session_id, id, checkpoint_id, delta_json, expires_at, authority_format
FROM chat_grants
ORDER BY created_at, chat_session_id, id;

-- name: DeleteChatGrant :execrows
DELETE FROM chat_grants WHERE id = sqlc.arg(id);

-- name: DeleteExpiredChatGrants :exec
DELETE FROM chat_grants
WHERE expires_at IS NOT NULL AND expires_at <= sqlc.arg(now);

-- Native authority is read only by its measured reader: the size comes first,
-- then the body from the same snapshot only when its identity still matches.

-- name: MeasureCheckpointAuthority :one
SELECT authority_format, authority_digest, CAST(length(CAST(authority_body AS BLOB)) AS INTEGER) AS body_bytes
FROM checkpoints WHERE id = ?;

-- name: ReadCheckpointAuthority :one
SELECT authority_body FROM checkpoints
WHERE id = sqlc.arg(id) AND authority_format = sqlc.arg(format) AND authority_digest = sqlc.arg(digest)
  AND length(CAST(authority_body AS BLOB)) = CAST(sqlc.arg(body_bytes) AS INTEGER);

-- name: MeasureApprovalOperationAuthority :one
SELECT authority_format, authority_digest, CAST(length(CAST(authority_body AS BLOB)) AS INTEGER) AS body_bytes
FROM approval_operations WHERE checkpoint_id = ?;

-- name: ReadApprovalOperationAuthority :one
SELECT authority_body FROM approval_operations
WHERE checkpoint_id = sqlc.arg(checkpoint_id) AND authority_format = sqlc.arg(format) AND authority_digest = sqlc.arg(digest)
  AND length(CAST(authority_body AS BLOB)) = CAST(sqlc.arg(body_bytes) AS INTEGER);

-- name: MeasureChatGrantAuthority :one
SELECT authority_format, authority_digest, CAST(length(CAST(authority_body AS BLOB)) AS INTEGER) AS body_bytes
FROM chat_grants WHERE chat_session_id = ? AND id = ?;

-- name: ReadChatGrantAuthority :one
SELECT authority_body FROM chat_grants
WHERE chat_session_id = sqlc.arg(chat_session_id) AND id = sqlc.arg(id) AND authority_format = sqlc.arg(format)
  AND authority_digest = sqlc.arg(digest) AND length(CAST(authority_body AS BLOB)) = CAST(sqlc.arg(body_bytes) AS INTEGER);
